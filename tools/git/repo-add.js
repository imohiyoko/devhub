// ── Repo Addition (directory browser / path input) ───
let userHome = '';
let isWindows = false;
let appConfig = { scan_roots: [], excludes: [], pinned_repos: [], repo_order: [], hidden_repos: [] };
let browsingPath = '~';
let browsingParent = null;
let repoHiddenExpanded = false;
let repoDragSrc = null;

async function loadSystemInfo() {
  try {
    const info = await fetch('/api/info').then(r => r.json());
    userHome = info.home || '';
    isWindows = !!info.is_windows;
  } catch(e) { console.error(e); }
}

async function loadAppConfig() {
  try {
    const cfg = await fetch('/api/config').then(r => r.json());
    appConfig.scan_roots = cfg.scan_roots || [];
    appConfig.excludes   = cfg.excludes   || [];
    appConfig.pinned_repos = cfg.pinned_repos || [];
    appConfig.repo_order   = cfg.repo_order   || [];
    appConfig.hidden_repos = cfg.hidden_repos || [];
  } catch(e) { console.error(e); }
}

async function saveAppConfig() {
  await fetch('/api/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      scan_roots:   appConfig.scan_roots,
      excludes:     appConfig.excludes,
      pinned_repos: appConfig.pinned_repos,
      repo_order:   appConfig.repo_order,
      hidden_repos: appConfig.hidden_repos,
    }),
  });
}

function toDisplayPath(absPath) {
  if (!userHome) return absPath;
  const na = absPath.replace(/\\/g, '/');
  const nh = userHome.replace(/\\/g, '/');
  const al = na.toLowerCase(), hl = nh.toLowerCase();
  if (al === hl) return '~';
  const prefix = hl.endsWith('/') ? hl : hl + '/';
  if (al.startsWith(prefix)) return '~/' + na.slice(prefix.length);
  return absPath;
}

function normPath(s) { return s.replace(/\\/g, '/'); }
function pathMatch(a, b) {
  if (!a || !b) return false;
  return isWindows
    ? normPath(a).toLowerCase() === normPath(b).toLowerCase()
    : normPath(a) === normPath(b);
}
function isInPinned(path) {
  const t = toDisplayPath(path);
  return appConfig.pinned_repos.some(p => pathMatch(p, path) || pathMatch(p, t));
}

// ── Repo switcher: ordering / hiding / rendering ──────
function isHidden(path) {
  const t = toDisplayPath(path);
  return appConfig.hidden_repos.some(p => pathMatch(p, path) || pathMatch(p, t));
}

// Sort repos by the saved repo_order (unordered items go to the end), mirroring
// the workspace tool's loadRepos().
function applyRepoOrder(repos) {
  const order = appConfig.repo_order || [];
  if (!order.length) return repos;
  return repos.slice().sort((a, b) => {
    const ai = order.indexOf(a.path);
    const bi = order.indexOf(b.path);
    if (ai === -1 && bi === -1) return 0;
    if (ai === -1) return 1;
    if (bi === -1) return -1;
    return ai - bi;
  });
}

function repoMatchesQuery(r, q) {
  if (!q) return true;
  return r.name.toLowerCase().includes(q) || r.path.toLowerCase().includes(q);
}

function renderRepoSwitcher() {
  const q = (document.getElementById('repo-search').value || '').trim().toLowerCase();
  const list = document.getElementById('repo-switcher-list');
  const hiddenWrap = document.getElementById('repo-hidden-wrap');

  // Button label reflects the current repo.
  const cur = allRepos.find(r => pathMatch(r.path, currentRepo));
  document.getElementById('repo-current-name').textContent = cur ? cur.name : 'Select Repo...';

  const visible = allRepos.filter(r => !isHidden(r.path) && repoMatchesQuery(r, q));
  const hidden  = allRepos.filter(r => isHidden(r.path)  && repoMatchesQuery(r, q));

  list.innerHTML = '';
  if (visible.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'repo-switcher-empty';
    empty.textContent = q ? '一致するリポジトリがありません' : '表示中のリポジトリがありません';
    list.appendChild(empty);
  } else {
    visible.forEach(r => list.appendChild(buildRepoRow(r, false)));
  }

  hiddenWrap.innerHTML = '';
  if (hidden.length > 0) {
    const toggle = document.createElement('button');
    toggle.className = 'repo-hidden-toggle';
    toggle.type = 'button';
    toggle.textContent = `${repoHiddenExpanded ? '▾' : '▸'} 非表示 (${hidden.length})`;
    toggle.addEventListener('click', (e) => {
      e.stopPropagation();
      repoHiddenExpanded = !repoHiddenExpanded;
      renderRepoSwitcher();
    });
    hiddenWrap.appendChild(toggle);
    if (repoHiddenExpanded) {
      const hlist = document.createElement('div');
      hlist.className = 'repo-hidden-list';
      hidden.forEach(r => hlist.appendChild(buildRepoRow(r, true)));
      hiddenWrap.appendChild(hlist);
    }
  }
}

function buildRepoRow(r, isHiddenRow) {
  const row = document.createElement('div');
  row.className = 'repo-row' + (!isHiddenRow && pathMatch(r.path, currentRepo) ? ' active' : '');
  row.dataset.path = r.path;
  // A hidden row re-pops (unhides) on click instead of silently selecting a repo
  // that stays hidden; a visible row selects it.
  row.addEventListener('click', () => isHiddenRow ? unhideRepo(r.path) : selectRepo(r.path));

  // Visible rows are draggable for reordering — but not while a search filter is
  // active, because the drop reorders the full allRepos list by index and moving
  // a filtered subset there is unintuitive.
  const searching = !!(document.getElementById('repo-search').value || '').trim();
  if (!isHiddenRow && !searching) {
    row.draggable = true;
    row.addEventListener('dragstart', (e) => onRepoDragStart(e, r.path));
    row.addEventListener('dragover', onRepoDragOver);
    row.addEventListener('dragleave', onRepoDragLeave);
    row.addEventListener('drop', (e) => onRepoDrop(e, r.path));
    const handle = document.createElement('span');
    handle.className = 'drag-handle';
    handle.textContent = '⠿';
    handle.title = 'ドラッグで並び替え';
    handle.addEventListener('click', (e) => e.stopPropagation());
    row.appendChild(handle);
  }

  const name = document.createElement('span');
  name.className = 'repo-name';
  name.textContent = r.name;
  name.title = r.name;
  row.appendChild(name);

  const pathEl = document.createElement('span');
  pathEl.className = 'repo-path';
  pathEl.textContent = toDisplayPath(r.path);
  pathEl.title = r.path;
  row.appendChild(pathEl);

  const btn = document.createElement('button');
  btn.type = 'button';
  if (isHiddenRow) {
    btn.className = 'repo-unhide-btn';
    btn.textContent = '戻す';
    btn.title = '表示に戻す';
    btn.addEventListener('click', (e) => { e.stopPropagation(); unhideRepo(r.path); });
  } else {
    btn.className = 'repo-hide-btn';
    btn.textContent = '✕';
    btn.title = '非表示にする';
    btn.addEventListener('click', (e) => { e.stopPropagation(); hideRepo(r.path); });
  }
  row.appendChild(btn);
  return row;
}

function selectRepo(path) {
  if (!path) return;
  currentRepo = path;
  saveCurrentRepo();
  // Don't carry the previous repo's remote state into the new one; the next
  // suggest refresh re-establishes it and adjustDynamicPolling re-arms.
  gitData.hasRemote = undefined;
  // Reset branch filters so they don't carry across repos.
  branchSearch = '';
  branchCommitterFilter = '';
  closeRepoSwitcher();
  renderRepoSwitcher();
  refreshData();
}

async function hideRepo(path) {
  if (!path) return;
  if (!isHidden(path)) appConfig.hidden_repos.push(path);
  await saveAppConfig();

  // Offer an immediate one-click undo so a repo hidden by mistake is trivial
  // to bring back, without having to open the 非表示 section.
  const repo = allRepos.find(r => pathMatch(r.path, path));
  const repoName = repo ? repo.name : (path.split(/[\\/]/).filter(Boolean).pop() || path);
  showUndoToast(`「${repoName}」を非表示にしました`, '元に戻す', () => unhideRepo(path));

  // If the active repo was just hidden, switch to the first remaining visible
  // one — or clear the selection (label back to "Select Repo...") if everything
  // is now hidden, so the header never shows a hidden repo as current.
  if (pathMatch(path, currentRepo)) {
    const next = allRepos.find(r => !isHidden(r.path));
    currentRepo = next ? next.path : '';
    saveCurrentRepo();
    gitData.hasRemote = undefined;
    renderRepoSwitcher();
    if (currentRepo) refreshData();
    return;
  }
  renderRepoSwitcher();
}

async function unhideRepo(path) {
  const t = toDisplayPath(path);
  appConfig.hidden_repos = appConfig.hidden_repos.filter(p => !pathMatch(p, path) && !pathMatch(p, t));
  await saveAppConfig();
  renderRepoSwitcher();
}

// Persist the current order of allRepos (visible + hidden) to repo_order.
async function saveRepoOrder() {
  appConfig.repo_order = allRepos.map(r => r.path);
  await saveAppConfig();
}

function onRepoDragStart(e, path) {
  repoDragSrc = path;
  e.dataTransfer.effectAllowed = 'move';
  e.currentTarget.style.opacity = '0.4';
}
function onRepoDragOver(e) {
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
  e.currentTarget.classList.add('drag-over');
}
function onRepoDragLeave(e) {
  e.currentTarget.classList.remove('drag-over');
}
function onRepoDrop(e, targetPath) {
  e.preventDefault();
  e.currentTarget.classList.remove('drag-over');
  if (!repoDragSrc || repoDragSrc === targetPath) { repoDragSrc = null; return; }
  const si = allRepos.findIndex(r => r.path === repoDragSrc);
  const ti = allRepos.findIndex(r => r.path === targetPath);
  if (si === -1 || ti === -1) { repoDragSrc = null; return; }
  const [moved] = allRepos.splice(si, 1);
  allRepos.splice(ti, 0, moved);
  repoDragSrc = null;
  saveRepoOrder();
  renderRepoSwitcher();
}

function showRepoMsg(text, type) {
  const el = document.getElementById('add-repo-msg');
  el.textContent = text;
  el.style.color = type === 'error' ? 'var(--red)' : type === 'warn' ? '#e3b341' : 'var(--green)';
  if (type !== 'error') setTimeout(() => { if (el.textContent === text) el.textContent = ''; }, 3000);
}

async function browseTo(path) {
  try {
    const res = await fetch(`/api/ls?path=${encodeURIComponent(path)}`);
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      showRepoMsg(err.error || '無効なパスです', 'error');
      return false;
    }
    const data = await res.json();
    browsingPath = data.path;
    browsingParent = data.parent;
    document.getElementById('path-input').value = browsingPath === '__drives__' ? 'マイコンピュータ' : toDisplayPath(browsingPath);
    document.getElementById('btn-dir-up').disabled = !data.parent;

    const list = document.getElementById('add-repo-dir-list');
    if (data.entries.length === 0) {
      list.innerHTML = '<div style="padding:20px;color:var(--muted);font-size:12px;text-align:center;">サブディレクトリなし</div>';
      return true;
    }

    const isDriveList = browsingPath === '__drives__';
    list.innerHTML = data.entries.map(e => {
      const icon = e.is_git
        ? '<span style="color:var(--green)">◈</span>'
        : isDriveList
          ? '<span style="color:var(--muted)">💽</span>'
          : '<span style="color:var(--muted)">📁</span>';
      const added = e.in_workspace;
      const addBtn = e.is_git
        ? `<button class="btn-pin ${added ? 'added' : ''}" data-path="${e.path.replace(/"/g, '&quot;')}">${added ? '追加済' : '追加'}</button>`
        : '';
      const arrow = '<span class="dir-arrow">›</span>';
      return `<div class="dir-entry" data-path="${e.path.replace(/"/g, '&quot;')}" data-is-git="${e.is_git}">
        <span class="dir-icon">${icon}</span>
        <span class="dir-name" title="${escapeHtml(e.path)}">${escapeHtml(e.name)}</span>
        <div class="dir-actions">${addBtn}${arrow}</div>
      </div>`;
    }).join('');

    // Attach click handlers
    list.querySelectorAll('.dir-entry').forEach(entry => {
      entry.addEventListener('click', (ev) => {
        browseTo(entry.dataset.path);
      });
    });
    list.querySelectorAll('.btn-pin').forEach(btn => {
      btn.addEventListener('click', (ev) => {
        ev.stopPropagation();
        if (!btn.classList.contains('added')) addRepoFromBrowser(btn.dataset.path, btn);
      });
    });
    return true;
  } catch(e) {
    console.error(e);
    showRepoMsg('ディレクトリの読み込みに失敗しました', 'error');
    return false;
  }
}

async function addRepoFromBrowser(path, btnEl) {
  const t = toDisplayPath(path);
  const alreadyPinned = appConfig.pinned_repos.some(p => pathMatch(p, path) || pathMatch(p, t));
  const isExcluded = appConfig.excludes.some(p => pathMatch(p, path) || pathMatch(p, t));
  if (alreadyPinned && !isExcluded) { showRepoMsg('既に追加されています', 'warn'); return; }
  if (isExcluded) appConfig.excludes = appConfig.excludes.filter(e => !pathMatch(e, path) && !pathMatch(e, t));
  if (!alreadyPinned) appConfig.pinned_repos.push(path);
  await saveAppConfig();
  await fetchRepos();
  if (btnEl) { btnEl.textContent = '追加済'; btnEl.classList.add('added'); }
  const name = path.split(/[\\/]/).filter(Boolean).pop() || path;
  showRepoMsg(`${name} を追加しました`, 'success');
}

async function handlePathSubmit() {
  const input = document.getElementById('path-input');
  const rawPath = input.value.trim();
  if (!rawPath) return;
  await browseTo(rawPath);
}

async function handlePathAdd() {
  const input = document.getElementById('path-input');
  const rawPath = input.value.trim();
  if (!rawPath) return;

  showRepoMsg('確認中...', 'success');
  try {
    const res = await fetch(`/api/ls?path=${encodeURIComponent(rawPath)}`);
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      showRepoMsg(err.error || '無効なパスです', 'error');
      return;
    }
    const data = await res.json();
    const targetPath = data.path;

    if (!data.is_git) {
      showRepoMsg('このフォルダは Git リポジトリではありません (.git が見つかりません)', 'error');
      return;
    }

    const t = toDisplayPath(targetPath);
    const alreadyPinned = appConfig.pinned_repos.some(p => pathMatch(p, targetPath) || pathMatch(p, t));
    const isExcluded = appConfig.excludes.some(p => pathMatch(p, targetPath) || pathMatch(p, t));
    if (alreadyPinned && !isExcluded) { showRepoMsg('既に追加されています', 'warn'); return; }
    if (isExcluded) appConfig.excludes = appConfig.excludes.filter(e => !pathMatch(e, targetPath) && !pathMatch(e, t));
    if (!alreadyPinned) appConfig.pinned_repos.push(targetPath);
    await saveAppConfig();
    await fetchRepos();
    const name = targetPath.split(/[\\/]/).filter(Boolean).pop() || targetPath;
    showRepoMsg(`${name} を追加しました`, 'success');
    browseTo(browsingPath);
  } catch(e) {
    showRepoMsg('追加に失敗しました', 'error');
  }
}

// ── Add-repo panel toggle ────────────────────────────
document.getElementById('btn-add-repo').addEventListener('click', () => {
  const panel = document.getElementById('add-repo-panel');
  const btn   = document.getElementById('btn-add-repo');
  const open  = panel.classList.toggle('open');
  btn.classList.toggle('active', open);
  if (open) {
    // Close settings panel if open
    document.getElementById('settings-panel').classList.remove('open');
    document.getElementById('btn-settings').classList.remove('active');
    browseTo(browsingPath);
    // Auto-focus the path input
    setTimeout(() => document.getElementById('path-input').focus(), 50);
  }
});

function closeAddRepoPanel() {
  document.getElementById('add-repo-panel').classList.remove('open');
  document.getElementById('btn-add-repo').classList.remove('active');
}
document.getElementById('btn-close-add-panel').addEventListener('click', closeAddRepoPanel);

document.getElementById('btn-dir-up').addEventListener('click', () => {
  if (browsingParent) browseTo(browsingParent);
});

document.getElementById('path-input').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); handlePathSubmit(); }
  if (e.key === 'Escape') { e.target.value = browsingPath === '__drives__' ? 'マイコンピュータ' : toDisplayPath(browsingPath); e.target.blur(); }
});
document.getElementById('path-input').addEventListener('blur', (e) => {
  const input = e.target;
  const panel = document.getElementById('add-repo-panel');
  if (e.relatedTarget && panel.contains(e.relatedTarget)) {
    return;
  }
  input.value = browsingPath === '__drives__' ? 'マイコンピュータ' : toDisplayPath(browsingPath);
});

document.getElementById('btn-path-add').addEventListener('click', () => handlePathAdd());

document.getElementById('btn-remove-repo').addEventListener('click', () => {
  if (!currentRepo) return;
  // Reversible hide — same as the per-row ✕. The repo moves to the 非表示
  // section and an "元に戻す" toast appears, so it is trivial to bring back
  // (no destructive exclude / re-add round-trip).
  hideRepo(currentRepo);
});

// (フォルダ選択ボタンは廃止)

