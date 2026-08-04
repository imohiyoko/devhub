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

// unitsOf returns the array this document edits: v2 environments define
// components, v1 environments define processes. Everything below works on
// whichever one it is, because a host_process component carries the process
// fields at its own top level.
function unitsOf(env) {
  return isV2Document() ? componentsOf(env) : (Array.isArray(env && env.processes) ? env.processes : []);
}

async function openProcessModal(envIndex, procIndex = -1) {
  currentEnvIndex = envIndex;
  currentProcIndex = procIndex;
  const v2 = isV2Document();
  const unit = v2 ? 'コンポーネント' : 'プロセス';
  document.getElementById('processModalTitle').textContent = procIndex >= 0 ? `${unit}の編集` : `${unit}の追加`;
  document.getElementById('procV2Fields').style.display = v2 ? '' : 'none';
  await fetchWorktrees();

  const env = envsData.environments[envIndex];
  const units = unitsOf(env);
  const current = procIndex >= 0 ? (units[procIndex] || {}) : {};
  const binding = current.binding || {};
  fillRepoSelect(document.getElementById('procBindRepo'), binding.repo_path || '', '(binding なし)', env.repos || []);
  fillBranchSelect(document.getElementById('procBindBranch'), binding.repo_path || '', binding.branch || '', '(branch を選択)');
  document.getElementById('procPortStrategy').value = current.port_strategy === 'offset' ? 'offset' : 'baton';
  document.getElementById('procPortEnvVar').value = current.port_env_var || '';

  // depends_on options: the siblings of the same kind of unit, minus self.
  const dependsSelect = document.getElementById('procDepends');
  dependsSelect.innerHTML = units
    .filter((_, i) => i !== procIndex) // exclude self
    .map(p => `<option value="${escapeHtml(p.id)}">${escapeHtml(p.label || p.id)}</option>`)
    .join('');

  document.getElementById('procId').value = current.id || '';
  document.getElementById('procLabel').value = current.label || '';
  document.getElementById('procCmd').value = current.command || '';
  document.getElementById('procCwd').value = current.cwd || '';
  document.getElementById('procPort').value = (current.port !== undefined && current.port !== null) ? current.port : '';
  document.getElementById('procDelay').value = typeof current.delay_seconds !== 'undefined' ? current.delay_seconds : 1;
  Array.from(dependsSelect.options).forEach(opt => {
    opt.selected = (current.depends_on || []).includes(opt.value);
  });
  // env は順序保持のため {key,value} の配列で保存している。
  tempEnvVars = (Array.isArray(current.env) ? current.env : []).map(v => ({
    key: String((v && v.key) ?? ''),
    value: String((v && v.value) ?? ''),
  }));
  // Fills the kind/lifecycle/compose inputs and picks which field group is
  // shown. For v1 that resolves to host_process, which is the only group v1
  // has — and the v2 rows above stay hidden.
  fillComponentFields(current);

  document.getElementById('bulkEnvArea').style.display = 'none';
  document.getElementById('bulkEnvText').value = '';
  renderEnvVars();
  document.getElementById('processModalOverlay').classList.add('open');
}

function closeProcessModal() {
  document.getElementById('processModalOverlay').classList.remove('open');
}

// readHostProcessFields validates and collects everything a host process needs.
// It is the same field set for a v1 process and a v2 host_process component, so
// both go through this one function rather than through two copies of the port,
// binding and port-strategy rules. Returns null after alerting on bad input.
function readHostProcessFields(id, targetEnv, depends_on) {
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
        return null;
      }
      if (Math.abs(b - a) + 1 > 1000) {
        alert('ポート範囲が広すぎます（最大 1000 ポート）。');
        return null;
      }
      port = `${Math.min(a, b)}-${Math.max(a, b)}`;
    } else if (/^\d+$/.test(portVal)) {
      const n = Number(portVal);
      if (!inRange(n)) {
        alert('ポートは 1〜65535 で入力してください。');
        return null;
      }
      port = n;
    } else {
      alert('ポートは 3000 または 3000-3010 の形式で入力してください。');
      return null;
    }
  }

  const bindRepo = document.getElementById('procBindRepo').value.trim();
  const bindBranch = document.getElementById('procBindBranch').value.trim();
  if (Boolean(bindRepo) !== Boolean(bindBranch)) {
    alert('Worktree binding は repo と branch の両方を指定してください（両方空なら binding なし）。');
    return null;
  }
  const envRepos = (targetEnv && targetEnv.repos) || [];
  if (envRepos.length && bindRepo && !envRepos.map(expandHome).includes(expandHome(bindRepo))) {
    alert('この binding の Repository は環境の許可スコープ外です。環境設定の「許可する Repository」に追加してください。');
    return null;
  }

  const strategy = document.getElementById('procPortStrategy').value;
  const portEnvVar = document.getElementById('procPortEnvVar').value.trim();
  if (strategy === 'offset') {
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(portEnvVar)) {
      alert('offset 戦略には採番先の env 変数名（例: PORT）が必要です。');
      return null;
    }
    if (port === undefined) {
      alert('offset 戦略には base ポートが必要です。');
      return null;
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

  return proc;
}

// saveProcess writes back whichever unit the document defines: a v1 process or
// a v2 component. The two never mix — a v2 document carrying a `processes` key
// is rejected outright by validateComponents.
async function saveProcess() {
  const v2 = isV2Document();
  const unit = v2 ? 'コンポーネント' : 'プロセス';
  const id = document.getElementById('procId').value.trim();
  if (!id) {
    alert(`${unit}IDを入力してください。`);
    return;
  }

  // Snapshot the indices: the save is awaited, and the modal stays open on
  // failure, so nothing should depend on the globals still pointing here.
  const envIndex = currentEnvIndex;
  const at = currentProcIndex;
  const targetEnv = envsData.environments[envIndex];
  // Also catches renaming a unit onto a sibling's id — a duplicate the server
  // would reject only after the whole document had been assembled.
  if (unitsOf(targetEnv).some((p, i) => p.id === id && i !== at)) {
    alert(`この${unit}IDは既に環境内に存在します。`);
    return;
  }

  const depends_on = Array.from(document.getElementById('procDepends').selectedOptions).map(opt => opt.value);
  const kind = v2 ? document.getElementById('procKind').value : 'host_process';

  let item;
  if (kind === 'compose_service') {
    const compose = readComposeFields();
    if (!compose) return;
    // A compose_service has no process view (no command, cwd or port), so none
    // of the host fields are read — a kind switch cannot leave one behind.
    item = {
      id: id,
      label: document.getElementById('procLabel').value.trim(),
      depends_on: depends_on,
      compose: compose,
    };
  } else {
    item = readHostProcessFields(id, targetEnv, depends_on);
    if (!item) return;
  }
  if (v2) {
    item.kind = kind;
    item.lifecycle = document.getElementById('procLifecycle').value;
  }

  const listKey = v2 ? 'components' : 'processes';
  const saved = await saveEnvEdit(envIndex, e => {
    if (!Array.isArray(e[listKey])) e[listKey] = [];
    if (at >= 0) e[listKey][at] = item;
    else e[listKey].push(item);
  });
  // Only close once the save landed: saveEnvEdit has already put the
  // environment back, so closing on failure would discard the user's input too.
  if (saved) closeProcessModal();
}

function deleteProcess(envIndex, procIndex) {
  if (!confirm('このプロセスを削除しますか？')) return;
  saveEnvEdit(envIndex, e => {
    if (Array.isArray(e.processes)) e.processes.splice(procIndex, 1);
  });
}
