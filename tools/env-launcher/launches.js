// --- Launched (running environments) ---
let launches = [];

async function fetchLaunches() {
  try {
    const res = await fetch('/api/envs/launches');
    const data = await res.json();
    if (!res.ok || data.error) throw new Error(data.error || 'failed');
    launches = data.launches || [];
    renderLaunches();
  } catch(e) {
    // Non-fatal: leave the previous list and keep polling.
    console.error('fetchLaunches', e);
  }
}

// A record is "dead" once no declared port is still listening. Worktrees are
// persistent (user-owned), so they no longer keep a record alive.
function isDeadLaunch(l) {
  const anyRunning = (l.processes || []).some(p => (p.live_ports || []).length);
  return !anyRunning;
}

function renderLaunches() {
  const wrap = document.getElementById('launchedWrap');
  const container = document.getElementById('launched');
  document.getElementById('launchedCount').textContent = launches.length;
  document.getElementById('clearDeadBtn').style.display =
    launches.some(isDeadLaunch) ? '' : 'none';
  if (!launches.length) {
    wrap.style.display = 'none';
    container.innerHTML = '';
    return;
  }
  wrap.style.display = 'block';

  container.innerHTML = launches.map(l => {
    const lid = escapeHtml(l.launch_id);
    const name = escapeHtml(l.env_name || l.env_id);
    const branch = l.branch ? `<span class="launch-tag">${escapeHtml(l.branch)}</span>` : '';
    const time = l.launched_at ? `<span class="launch-time">${escapeHtml(l.launched_at)}</span>` : '';

    let meta = '';
    if (l.worktree_path) {
      meta = l.worktree_exists
        ? `<div class="launch-meta">worktree: ${escapeHtml(l.worktree_path)}</div>`
        : `<div class="launch-meta gone">worktree: ${escapeHtml(l.worktree_path)} (削除済み)</div>`;
    } else {
      meta = `<div class="launch-meta">worktree なし</div>`;
    }

    const procs = (l.processes || []).map(p => {
      const pname = escapeHtml(p.label || p.id);
      const hasPort = (p.port !== undefined && p.port !== null && p.port !== '');
      // offset processes were assigned a free port; show base → assigned.
      const portTxt = hasPort
        ? `<span class="proc-port">:${escapeHtml(p.port)}${p.assigned_port ? ` → :${escapeHtml(p.assigned_port)}` : ''}</span>`
        : '';
      const branchTag = p.branch ? `<span class="launch-tag">${escapeHtml(p.branch)}</span>` : '';
      const live = p.live_ports || [];
      let badge = '';
      let killBtns = '';
      if (hasPort) {
        if (live.length) {
          // Show the port(s) the process actually bound (may differ from the
          // declared port when the dev server auto-picked the next free one).
          const actual = live.map(lp => lp.port).join(', ');
          badge = `<span class="run-badge up">稼働中: ${escapeHtml(actual)}</span>`;
          killBtns = live.map(lp =>
            `<button class="btn btn-sm btn-danger" data-laction="kill" data-port="${escapeHtml(lp.port)}" data-pid="${escapeHtml(lp.pid)}">kill :${escapeHtml(lp.port)}</button>`
          ).join('');
        } else {
          badge = `<span class="run-badge down">停止</span>`;
        }
      } else {
        badge = `<span class="run-badge" title="ポート未宣言のため稼働状況を追跡できません">port未宣言</span>`;
      }
      return `<div class="launch-proc">
        <span class="proc-name">${pname}</span>${portTxt}${branchTag}
        <span class="spacer"></span>${badge}${killBtns}
      </div>`;
    }).join('');

    const hasWt = l.worktree_path && l.worktree_exists;
    const openBtns = hasWt ? `
      <button class="btn btn-sm" data-laction="open-editor" data-id="${lid}">エディタで開く</button>
      <button class="btn btn-sm" data-laction="open-terminal" data-id="${lid}">ターミナルで開く</button>` : '';
    // Worktrees are user-owned and never deleted from here, so removal only
    // clears the tracking record.
    const removeLabel = '記録を削除';

    return `<div class="launch-card">
      <div class="launch-head"><h3>${name}</h3>${branch}${time}</div>
      ${meta}
      ${procs ? `<div class="launch-procs">${procs}</div>` : ''}
      <div class="launch-actions">
        ${openBtns}
        <button class="btn btn-sm btn-danger" data-laction="remove" data-id="${lid}">${removeLabel}</button>
      </div>
    </div>`;
  }).join('');
}

async function postLaunchAction(url, body, okMsg) {
  try {
    const res = await fetch(url, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(body)
    });
    const data = await res.json();
    if (!res.ok || data.error) throw new Error(data.error || 'failed');
    if (okMsg) showToast(okMsg);
    return true;
  } catch(e) {
    return { error: e.message };
  }
}

async function killLaunchProc(port, pid) {
  if (!confirm(`port ${port} / pid ${pid} を終了しますか？`)) return;
  const r = await postLaunchAction('/api/ports/kill', { port, pid }, 'kill しました');
  if (r && r.error) alert('Error: ' + r.error);
  setTimeout(fetchLaunches, 600);
}

async function removeLaunch(id) {
  // Removing a launch only clears the tracking record. The worktree itself is
  // left intact (manage it in the git tool).
  if (!confirm('この起動記録を削除します（worktree は残ります）。よろしいですか？')) return;
  const r = await postLaunchAction('/api/envs/launches/remove', { launch_id: id }, '記録を削除しました');
  if (r && r.error) alert('Error: ' + r.error);
  fetchLaunches();
}

async function openLaunch(id, target) {
  const r = await postLaunchAction('/api/envs/launches/open', { launch_id: id, target },
    target === 'editor' ? 'エディタで開きました' : 'ターミナルで開きました');
  if (r && r.error) alert('Error: ' + r.error);
}

async function clearDeadLaunches() {
  const dead = launches.filter(isDeadLaunch);
  if (!dead.length) return;
  if (!confirm(`停止中の記録 ${dead.length} 件を削除します。よろしいですか？`)) return;
  for (const l of dead) {
    const r = await postLaunchAction('/api/envs/launches/remove', { launch_id: l.launch_id }, null);
    if (r && r.error) console.error('clearDead', l.launch_id, r.error);
  }
  showToast('停止中の記録をクリアしました');
  fetchLaunches();
}

document.getElementById('launched').addEventListener('click', e => {
  const btn = e.target.closest('[data-laction]');
  if (!btn) return;
  const a = btn.dataset.laction;
  if (a === 'kill') killLaunchProc(Number(btn.dataset.port), Number(btn.dataset.pid));
  else if (a === 'remove') removeLaunch(btn.dataset.id);
  else if (a === 'open-editor') openLaunch(btn.dataset.id, 'editor');
  else if (a === 'open-terminal') openLaunch(btn.dataset.id, 'terminal');
});

function showToast(message) {
  const toast = document.createElement('div');
  toast.textContent = message;
  toast.style.position = 'fixed';
  toast.style.bottom = '20px';
  toast.style.right = '20px';
  toast.style.backgroundColor = 'var(--accent)';
  toast.style.color = '#fff';
  toast.style.padding = '12px 20px';
  toast.style.borderRadius = '8px';
  toast.style.boxShadow = '0 4px 6px rgba(0,0,0,0.1)';
  toast.style.zIndex = '9999';
  toast.style.opacity = '0';
  toast.style.transition = 'opacity 0.3s ease';
  document.body.appendChild(toast);

  // Fade in
  setTimeout(() => toast.style.opacity = '1', 10);

  // Fade out and remove
  setTimeout(() => {
    toast.style.opacity = '0';
    setTimeout(() => toast.remove(), 300);
  }, 3000);
}
