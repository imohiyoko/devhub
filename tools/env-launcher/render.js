// --- Render ---

// The stored document's schema version decides what a card can offer: v1 edits
// processes, v2 shows components and switches scenarios. Only an exact 2 counts,
// mirroring the backend's strict version gate.
function isV2Document() { return envsData.version === 2; }

// switchSectionHtml renders one environment's components, plus the scenarios it
// can be switched to.
//
// The list itself comes from the stored config, not from /api/envs/state: that
// is what gives every row an index the editor can act on, and it means the
// components show up immediately instead of behind a "loading" placeholder.
// State (the dot and its reason) is layered on by id once /api/envs/state
// arrives — a component that was just added is simply not in it yet.
function switchSectionHtml(env, eIdx) {
  const state = switchEnvState(env.id);
  const observed = {};
  ((state && state.components) || []).forEach(c => { observed[c.id] = c; });

  const envId = escapeHtml(env.id);
  const components = componentsOf(env);
  const rows = components.length
    ? components.map((c, cIdx) => {
        const seen = observed[c.id] || {};
        const stateName = seen.state || 'unknown';
        const why = seen.reason || (state ? '' : '状態を読み込み中');
        const reason = why ? ` — ${escapeHtml(why)}` : '';
        const shared = c.lifecycle === 'shared' ? ' <span class="component-tag">shared</span>' : '';
        const kind = c.kind === 'compose_service' ? 'compose_service' : 'host_process';
        return `
      <div class="component-item">
        <span class="state-dot state-${escapeHtml(stateName)}" title="${escapeHtml(why || stateName)}"></span>
        <div class="component-info">
          <div class="component-label">${escapeHtml(c.label || c.id)}${shared}</div>
          <div class="component-meta">${escapeHtml(kind)}${reason}</div>
        </div>
        <div class="process-actions">
          <button class="btn" data-action="edit-component" data-e-idx="${eIdx}" data-c-idx="${cIdx}">編集</button>
          <button class="btn" data-action="delete-component" data-e-idx="${eIdx}" data-c-idx="${cIdx}" style="color: var(--red);">削除</button>
        </div>
      </div>`;
      }).join('')
    : '<div class="empty" style="padding: 20px;">コンポーネントがありません</div>';

  // Switching is only meaningful with somewhere to switch to; a single
  // scenario still gets the state list and the stop action.
  const scenarios = ((state && state.scenarios) || []).map(s => {
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
      <div style="margin-top: 4px;">
        <button class="btn" data-action="add-component" data-e-idx="${eIdx}">＋ コンポーネントを追加</button>
      </div>
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
    // A v2 document defines its units as components: the same modal edits
    // them, but the card offers the scenario switcher rather than the
    // per-process launch buttons, which have no meaning for a compose service.
    if (isV2Document()) {
      html += runtimeSectionHtml(env, eIdx);
      html += switchSectionHtml(env, eIdx);
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
