// --- Process Modal ---
let tempEnvVars = [];

function renderEnvVars() {
  const container = document.getElementById('procEnvVars');
  container.innerHTML = tempEnvVars.map((v, i) => {
    const escapedKey = escapeHtml(v.key);
    const escapedValue = escapeHtml(v.value);
    return `
    <div class="env-var-row" data-key="${i}">
      <span class="drag-handle env-var-drag-handle" title="ドラッグで並び替え">⠿</span>
      <input type="text" class="form-control" placeholder="KEY" value="${escapedKey}" oninput="updateEnvVarKey(${i}, this.value, this.nextElementSibling)">
      <input type="${isMaskedKey(v.key) ? 'password' : 'text'}" class="form-control" placeholder="VALUE" value="${escapedValue}" oninput="updateEnvVarValue(${i}, this.value)">
      <button class="env-var-remove" onclick="removeEnvVar(${i})">✕</button>
    </div>
  `}).join('');
  // 行のドラッグ並び替え（インデックスを key に使用）。保存はプロセス保存時。
  DevhubReorder.attach(container, {
    itemSelector: '.env-var-row', keyAttr: 'data-key',
    handleSelector: '.env-var-drag-handle', onDrop: reorderEnvVars,
  });
}
function reorderEnvVars(srcKey, dstKey) {
  const order = DevhubReorder.move(tempEnvVars.map((_, i) => String(i)), srcKey, dstKey);
  tempEnvVars = order.map(i => tempEnvVars[Number(i)]);
  renderEnvVars();
}
function addEnvVar() { tempEnvVars.push({key: '', value: ''}); renderEnvVars(); }
function updateEnvVarKey(i, val, valueInputEl) {
  tempEnvVars[i].key = val;
  if (valueInputEl) {
    valueInputEl.type = isMaskedKey(val) ? 'password' : 'text';
  }
}
function updateEnvVarValue(i, val) { tempEnvVars[i].value = val; }
function removeEnvVar(i) { tempEnvVars.splice(i, 1); renderEnvVars(); }

function toggleBulkEnv() {
  const area = document.getElementById('bulkEnvArea');
  const open = area.style.display === 'none';
  area.style.display = open ? 'block' : 'none';
  if (open) document.getElementById('bulkEnvText').focus();
}

// export ブロック / .env 形式のテキストを {key, value} 配列にパースする。
// - `export ` プレフィックスは除去
// - 空行と `#` 始まりのコメント行は無視
// - 値を囲む対の "..." / '...' は除去
// - キーが識別子として不正な行はスキップ
function parseEnvBlock(text) {
  const out = [];
  text.split(/\r?\n/).forEach(raw => {
    let line = raw.trim();
    if (!line || line.startsWith('#')) return;
    // `export` の後続区切りはスペース1個に限らず、タブや複数スペースも許容する。
    line = line.replace(/^export\s+/, '');
    const eq = line.indexOf('=');
    if (eq <= 0) return;
    const key = line.slice(0, eq).trim();
    let val = line.slice(eq + 1).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) return;
    if (val.length >= 2 &&
        ((val[0] === '"' && val.endsWith('"')) || (val[0] === "'" && val.endsWith("'")))) {
      val = val.slice(1, -1);
    }
    out.push({ key, value: val });
  });
  return out;
}

function applyBulkEnv() {
  const text = document.getElementById('bulkEnvText').value;
  const parsed = parseEnvBlock(text);
  if (parsed.length === 0) {
    alert('パースできる環境変数がありませんでした。`KEY=value` 形式で貼り付けてください。');
    return;
  }
  // 既存キーは上書き、新規キーは追加（重複キーは一意化）。
  // find による先頭一致のみの更新だと既存の重複行が残り、保存時の後勝ちで
  // 古い値に巻き戻るため、トリム済みキーで Map に正規化してから組み立てる。
  const merged = new Map();
  tempEnvVars.forEach(({ key, value }) => {
    const normalized = key.trim();
    if (normalized) merged.set(normalized, { key: normalized, value });
  });
  parsed.forEach(({ key, value }) => {
    merged.set(key, { key, value });
  });
  tempEnvVars = Array.from(merged.values());
  document.getElementById('bulkEnvText').value = '';
  toggleBulkEnv();
  renderEnvVars();
}

async function openProcessModal(envIndex, procIndex = -1) {
  currentEnvIndex = envIndex;
  currentProcIndex = procIndex;
  document.getElementById('processModalTitle').textContent = procIndex >= 0 ? 'プロセスの編集' : 'プロセスの追加';
  await fetchWorktrees();

  const env = envsData.environments[envIndex];
  const procForBind = procIndex >= 0 ? (env.processes[procIndex] || {}) : {};
  const binding = procForBind.binding || {};
  fillRepoSelect(document.getElementById('procBindRepo'), binding.repo_path || '', '(binding なし)', env.repos || []);
  fillBranchSelect(document.getElementById('procBindBranch'), binding.repo_path || '', binding.branch || '', '(branch を選択)');
  document.getElementById('procPortStrategy').value = procForBind.port_strategy === 'offset' ? 'offset' : 'baton';
  document.getElementById('procPortEnvVar').value = procForBind.port_env_var || '';

  // Populate depends_on options
  const dependsSelect = document.getElementById('procDepends');
  dependsSelect.innerHTML = (env.processes || [])
    .filter((_, i) => i !== procIndex) // exclude self
    .map(p => `<option value="${escapeHtml(p.id)}">${escapeHtml(p.label || p.id)}</option>`)
    .join('');

  if (procIndex >= 0) {
    const proc = env.processes[procIndex];
    document.getElementById('procId').value = proc.id || '';
    document.getElementById('procLabel').value = proc.label || '';
    document.getElementById('procCmd').value = proc.command || '';
    document.getElementById('procCwd').value = proc.cwd || '';
    document.getElementById('procPort').value = (proc.port !== undefined && proc.port !== null) ? proc.port : '';
    document.getElementById('procDelay').value = typeof proc.delay_seconds !== 'undefined' ? proc.delay_seconds : 1;

    Array.from(dependsSelect.options).forEach(opt => {
      opt.selected = (proc.depends_on || []).includes(opt.value);
    });

    // env は順序保持のため {key,value} の配列で保存している。
    tempEnvVars = (Array.isArray(proc.env) ? proc.env : []).map(v => ({
      key: String((v && v.key) ?? ''),
      value: String((v && v.value) ?? ''),
    }));
  } else {
    document.getElementById('procId').value = '';
    document.getElementById('procLabel').value = '';
    document.getElementById('procCmd').value = '';
    document.getElementById('procCwd').value = '';
    document.getElementById('procPort').value = '';
    document.getElementById('procDelay').value = 1;
    Array.from(dependsSelect.options).forEach(opt => opt.selected = false);
    tempEnvVars = [];
  }

  document.getElementById('bulkEnvArea').style.display = 'none';
  document.getElementById('bulkEnvText').value = '';
  renderEnvVars();
  document.getElementById('processModalOverlay').classList.add('open');
}

function closeProcessModal() {
  document.getElementById('processModalOverlay').classList.remove('open');
}

function saveProcess() {
  const id = document.getElementById('procId').value.trim();
  if (!id) {
    alert("プロセスIDを入力してください。");
    return;
  }

  const targetEnv = envsData.environments[currentEnvIndex];
  if (currentProcIndex === -1 && targetEnv.processes && targetEnv.processes.some(p => p.id === id)) {
    alert("このプロセスIDは既に環境内に存在します。");
    return;
  }

  const dependsSelect = document.getElementById('procDepends');
  const depends_on = Array.from(dependsSelect.selectedOptions).map(opt => opt.value);

  // env は {key,value} の配列で保存し入力順を保持する（オブジェクトだと
  // Go の json.Marshal がキーをソートしてしまい順序が崩れる）。
  const envList = tempEnvVars
    .map(v => ({key: String(v.key ?? '').trim(), value: String(v.value ?? '')}))
    .filter(v => v.key);

  const delayVal = document.getElementById('procDelay').value.trim();
  const portVal = document.getElementById('procPort').value.trim();
  let port;
  if (portVal !== '') {
    const inRange = n => Number.isInteger(n) && n >= 1 && n <= 65535;
    const rangeMatch = portVal.match(/^(\d+)\s*-\s*(\d+)$/);
    if (rangeMatch) {
      const a = Number(rangeMatch[1]), b = Number(rangeMatch[2]);
      if (!inRange(a) || !inRange(b)) {
        alert('ポート範囲は 1〜65535 で指定してください。');
        return;
      }
      if (Math.abs(b - a) + 1 > 1000) {
        alert('ポート範囲が広すぎます（最大 1000 ポート）。');
        return;
      }
      port = `${Math.min(a, b)}-${Math.max(a, b)}`;
    } else if (/^\d+$/.test(portVal)) {
      const n = Number(portVal);
      if (!inRange(n)) {
        alert('ポートは 1〜65535 で入力してください。');
        return;
      }
      port = n;
    } else {
      alert('ポートは 3000 または 3000-3010 の形式で入力してください。');
      return;
    }
  }

  const bindRepo = document.getElementById('procBindRepo').value.trim();
  const bindBranch = document.getElementById('procBindBranch').value.trim();
  if (Boolean(bindRepo) !== Boolean(bindBranch)) {
    alert('Worktree binding は repo と branch の両方を指定してください（両方空なら binding なし）。');
    return;
  }
  const envRepos = (targetEnv && targetEnv.repos) || [];
  if (envRepos.length && bindRepo && !envRepos.map(expandHome).includes(expandHome(bindRepo))) {
    alert('この binding の Repository は環境の許可スコープ外です。環境設定の「許可する Repository」に追加してください。');
    return;
  }

  const strategy = document.getElementById('procPortStrategy').value;
  const portEnvVar = document.getElementById('procPortEnvVar').value.trim();
  if (strategy === 'offset') {
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(portEnvVar)) {
      alert('offset 戦略には採番先の env 変数名（例: PORT）が必要です。');
      return;
    }
    if (port === undefined) {
      alert('offset 戦略には base ポートが必要です。');
      return;
    }
  }

  const proc = {
    id: id,
    label: document.getElementById('procLabel').value.trim(),
    command: document.getElementById('procCmd').value.trim(),
    cwd: document.getElementById('procCwd').value.trim(),
    delay_seconds: delayVal === '' ? 1.0 : (parseFloat(delayVal) || 0),
    depends_on: depends_on,
    env: envList
  };
  if (port !== undefined) proc.port = port;
  if (bindRepo && bindBranch) proc.binding = { repo_path: bindRepo, branch: bindBranch };
  if (strategy === 'offset') {
    proc.port_strategy = 'offset';
    proc.port_env_var = portEnvVar;
  }

  if (!proc.id) { alert('IDは必須です'); return; }

  const env = envsData.environments[currentEnvIndex];
  if (!env.processes) env.processes = [];

  if (currentProcIndex >= 0) {
    env.processes[currentProcIndex] = proc;
  } else {
    env.processes.push(proc);
  }

  closeProcessModal();
  saveEnvsData();
}

function deleteProcess(envIndex, procIndex) {
  if (confirm('このプロセスを削除しますか？')) {
    envsData.environments[envIndex].processes.splice(procIndex, 1);
    saveEnvsData();
  }
}
