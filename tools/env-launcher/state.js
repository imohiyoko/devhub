let envsData = { environments: [] };
// Guard against overwriting the stored envs with the empty default when the
// initial GET /api/envs failed (e.g. the page loaded during a server
// rebuild/restart). saveEnvsData() does a full-document replace, so a save
// issued before a successful load would wipe every environment. Only flip this
// true once a load has actually populated envsData.
let envsLoaded = false;
let currentEnvIndex = -1;
let currentProcIndex = -1;

// Cross-repo worktree inventory (source of truth = git). Used to populate the
// repo/branch pickers so users reference existing worktrees instead of typing.
let worktreeData = { repos: [], home: '' };

async function fetchWorktrees() {
  try {
    const res = await fetch('/api/envs/worktrees');
    const data = await res.json();
    if (res.ok && !data.error) worktreeData = data;
  } catch (e) { /* non-fatal: pickers just stay empty */ }
}

// Expand a leading ~ using the home dir reported by the backend, so env-declared
// repo paths (which may be ~-relative) compare equal to the scanner's absolute
// paths — mirroring the backend's ExpandUser on both sides of the scope check.
function expandHome(p) {
  const home = worktreeData.home;
  if (!p || !home || home === '~') return p;
  if (p === '~') return home;
  if (p.startsWith('~/')) return home + '/' + p.slice(2);
  return p;
}

function repoByPath(path) {
  return (worktreeData.repos || []).find(r => r.path === path);
}

// Branches that have an existing, on-disk worktree in the given repo.
function branchesForRepo(path) {
  const repo = repoByPath(path);
  if (!repo) return [];
  return (repo.worktrees || [])
    .filter(w => w.branch && w.exists)
    .map(w => w.branch);
}

// Fill a repo <select>, keeping `selected` if still present. When `allowed` is
// a non-empty list of repo paths, only those repos are offered — this enforces
// the environment's declared repo scope in the picker.
function fillRepoSelect(sel, selected, placeholder, allowed) {
  let repos = worktreeData.repos || [];
  if (allowed && allowed.length) {
    const set = new Set(allowed.map(expandHome));
    repos = repos.filter(r => set.has(r.path));
  }
  const opts = [`<option value="">${placeholder || '(なし)'}</option>`]
    .concat(repos.map(r => `<option value="${escapeHtml(r.path)}">${escapeHtml(r.name)}</option>`));
  sel.innerHTML = opts.join('');
  sel.value = selected || '';
  // A previously-saved repo that no longer scans (or is now out of scope) still
  // needs to show so the user can see and clear it.
  if (selected && sel.value !== selected) {
    sel.insertAdjacentHTML('beforeend', `<option value="${escapeHtml(selected)}">${escapeHtml(selected)} (未検出)</option>`);
    sel.value = selected;
  }
}

// Fill the env-scope repo multi-select, marking `selectedPaths` as selected.
// Saved paths no longer in the inventory are still shown so they aren't lost.
function fillReposMulti(sel, selectedPaths) {
  const repos = worktreeData.repos || [];
  const chosen = new Set((selectedPaths || []).map(expandHome));
  const opts = repos.map(r =>
    `<option value="${escapeHtml(r.path)}"${chosen.has(r.path) ? ' selected' : ''}>${escapeHtml(r.name)}</option>`);
  (selectedPaths || []).forEach(p => {
    if (!repos.some(r => r.path === expandHome(p))) {
      opts.push(`<option value="${escapeHtml(p)}" selected>${escapeHtml(p)} (未検出)</option>`);
    }
  });
  sel.innerHTML = opts.join('');
}

// Fill a branch <select> from a repo's existing worktrees.
function fillBranchSelect(sel, repoPath, selected, placeholder) {
  const branches = branchesForRepo(repoPath);
  const opts = [`<option value="">${placeholder || '(なし)'}</option>`]
    .concat(branches.map(b => `<option value="${escapeHtml(b)}">${escapeHtml(b)}</option>`));
  sel.innerHTML = opts.join('');
  sel.value = selected || '';
  if (selected && sel.value !== selected) {
    sel.insertAdjacentHTML('beforeend', `<option value="${escapeHtml(selected)}">${escapeHtml(selected)} (worktree 未作成)</option>`);
    sel.value = selected;
  }
}
