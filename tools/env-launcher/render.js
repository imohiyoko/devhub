// --- Render ---

// The stored document's schema version decides what a card can offer: v1 edits
// processes, v2 shows components and switches scenarios. Only an exact 2 counts,
// mirroring the backend's strict version gate.
function isV2Document() { return envsData.version === 2; }

// switchSectionHtml renders one environment's components with their observed
// state, plus the scenarios it can be switched to. State comes from
// /api/envs/state (switch.js), which may not have arrived yet.
function switchSectionHtml(env) {
  const state = switchEnvState(env.id);
  if (!state) {
    return '<div class="empty" style="padding: 20px;">状態を読み込み中...</div>';
  }
  const components = state.components || [];
  if (!components.length) {
    return '<div class="empty" style="padding: 20px;">コンポーネントがありません</div>';
  }
  const envId = escapeHtml(env.id);
  const rows = components.map(c => {
    const reason = c.reason ? ` — ${escapeHtml(c.reason)}` : '';
    const shared = c.shared ? ' <span class="component-tag">shared</span>' : '';
    return `
      <div class="component-item">
        <span class="state-dot state-${escapeHtml(c.state)}" title="${escapeHtml(c.reason || c.state)}"></span>
        <div class="component-info">
          <div class="component-label">${escapeHtml(c.label)}${shared}</div>
          <div class="component-meta">${escapeHtml(c.kind)}${reason}</div>
        </div>
      </div>`;
  }).join('');

  // Switching is only meaningful with somewhere to switch to; a single
  // scenario still gets the state list and the stop action.
  const scenarios = (state.scenarios || []).map(s => {
    const name = escapeHtml(s.name || s.id);
    return `<button class="btn btn-sm" data-action="switch-scenario" data-env-id="${envId}" data-scenario-id="${escapeHtml(s.id)}" data-scenario-name="${name}">${name}</button>`;
  }).join('');
  const actions = scenarios
    ? `<div class="switch-actions">${scenarios}<button class="btn btn-sm" data-action="switch-stop" data-env-id="${envId}">全停止</button></div>`
    : '';
  return `
    <div class="switch-section">
      <div class="switch-head">コンポーネント${actions}</div>
      ${rows}
    </div>`;
}

function isMaskedKey(key) {
  const lower = key.toLowerCase();
  return lower.includes('password') || lower.includes('secret') || lower.includes('token') || lower.includes('apikey') || lower.includes('api_key') || lower.includes('api-key');
}

function render() {
  const app = document.getElementById('app');
  if (!envsData.environments || envsData.environments.length === 0) {
    app.innerHTML = '<div class="empty">環境がありません。「＋ 環境を追加」から作成してください。</div>';
    return;
  }

  let html = '';
  envsData.environments.forEach((env, eIdx) => {
    const envId = escapeHtml(env.id);
    const envName = escapeHtml(env.name || env.id);
    html += `
      <div class="env-card" data-key="${eIdx}">
        <div class="env-header">
          <span class="drag-handle env-drag-handle" title="ドラッグで環境を並び替え">⠿</span>
          <h2>${envName}</h2>
          ${env.worktree?.enabled ? `<span style="font-size: 11px; background: var(--border); padding: 2px 6px; border-radius: 4px;">worktree</span>` : ''}
          <div class="env-actions">
            <button class="btn btn-success" data-action="launch-env" data-env-id="${envId}">▶ 全て起動</button>
            <button class="btn" data-action="edit-env" data-e-idx="${eIdx}">編集</button>
            <button class="btn" data-action="delete-env" data-e-idx="${eIdx}" style="color: var(--red);">削除</button>
          </div>
        </div>
        <div class="env-body">
    `;
    // A v2 document defines its units as components, which this UI can show
    // and switch but not yet edit (#150) — so it offers the switcher instead
    // of a process editor that could not save.
    if (isV2Document()) {
      html += switchSectionHtml(env);
      html += `</div></div>`;
      return;
    }
    html += `
          <div style="margin-bottom: 12px;">
            <button class="btn" data-action="add-process" data-e-idx="${eIdx}">＋ プロセスを追加</button>
          </div>
    `;
    if (env.processes && env.processes.length > 0) {
      env.processes.forEach((proc, pIdx) => {
        const procId = escapeHtml(proc.id);
        const procLabel = escapeHtml(proc.label || proc.id);
        const procCmd = escapeHtml(proc.command);
        const procCwd = escapeHtml(proc.cwd);
        const procDepends = proc.depends_on && proc.depends_on.length > 0
          ? ` | depends_on: ${escapeHtml(proc.depends_on.join(', '))}`
          : '';
        const procDelay = typeof proc.delay_seconds !== 'undefined'
          ? ` | delay: ${escapeHtml(proc.delay_seconds)}s`
          : '';
        const procPort = (proc.port !== undefined && proc.port !== null)
          ? ` | port: ${escapeHtml(proc.port)}`
          : '';

        html += `
          <div class="process-item" data-key="${pIdx}">
            <span class="drag-handle proc-drag-handle" title="ドラッグでプロセスを並び替え">⠿</span>
            <div class="process-info">
              <div class="process-label">${procLabel}</div>
              <div class="process-cmd">${procCmd}</div>
              <div class="process-meta">
                cwd: ${procCwd}
                ${procPort}
                ${procDepends}
                ${procDelay}
              </div>
            </div>
            <div class="process-actions">
              <button class="btn" data-action="launch-process" data-env-id="${envId}" data-proc-id="${procId}">▶ 起動</button>
              <button class="btn" data-action="edit-process" data-e-idx="${eIdx}" data-p-idx="${pIdx}">編集</button>
              <button class="btn" data-action="delete-process" data-e-idx="${eIdx}" data-p-idx="${pIdx}" style="color: var(--red);">削除</button>
            </div>
          </div>
        `;
      });
    } else {
      html += '<div class="empty" style="padding: 20px;">プロセスがありません</div>';
    }
    html += `</div></div>`;
  });
  app.innerHTML = html;

  // ドラッグ並び替えを配線（共通モジュール）。環境カードは app 直下、
  // プロセスは各環境カード内に限定して attach（環境をまたぐ移動を防ぐ）。
  DevhubReorder.attach(app, {
    itemSelector: '.env-card', keyAttr: 'data-key',
    handleSelector: '.env-drag-handle', onDrop: reorderEnvironments,
  });
  app.querySelectorAll('.env-card').forEach(card => {
    DevhubReorder.attach(card, {
      itemSelector: '.process-item', keyAttr: 'data-key',
      handleSelector: '.proc-drag-handle',
      onDrop: (src, dst) => reorderProcesses(card.dataset.key, src, dst),
    });
  });
}

// 環境カードの並び替え：id 配列を並び替え→配列を再構築→保存（保存成功時に再描画）。
function reorderEnvironments(src, dst) {
  const envs = envsData.environments || [];
  const order = DevhubReorder.move(envs.map((_, i) => String(i)), src, dst);
  envsData.environments = order.map(i => envs[Number(i)]);
  saveEnvsData();
}

// プロセスの並び替え：対象環境内でのみ。配列保存なので順序は永続化される。
function reorderProcesses(envId, src, dst) {
  const env = (envsData.environments || []).find(e => e.id === envId);
  if (!env || !env.processes) return;
  const procs = env.processes;
  const order = DevhubReorder.move(procs.map((_, i) => String(i)), src, dst);
  env.processes = order.map(i => procs[Number(i)]);
  saveEnvsData();
}
