const TOOL_ID = 'git';
const DEFAULT_CONFIG = {
  default_repo: '',
  layout: { left_width: 280, show_log: true, log_height: 180 },
  log_limit: 100,
  keybindings: { stage: ' ', commit: 'c', push: 'P', pull: 'p', new_branch: 'n', refresh: 'r' },
  commit_template: '',
  local_poll_interval: 'auto',
  remote_poll_interval: 'auto',
};

let gitConfig = structuredClone(DEFAULT_CONFIG);
let allRepos = [];
let currentRepo = '';

// The selected repo is persisted in localStorage so a reload (Cmd+R) keeps the
// repo currently in view instead of jumping back to the default / first repo.
const REPO_STORAGE_KEY = 'git-tool-current-repo';
function loadSavedRepo() {
    try { return localStorage.getItem(REPO_STORAGE_KEY) || ''; } catch (e) { return ''; }
}
function saveCurrentRepo() {
    try { localStorage.setItem(REPO_STORAGE_KEY, currentRepo); } catch (e) {}
}

// Active tab is persisted in localStorage so a reload (Cmd+R) keeps the
// current tab instead of resetting to the default.
const TAB_STORAGE_KEY = 'git-tool-active-tab';
const VALID_TABS = ['status', 'branches', 'commits', 'stash', 'worktrees'];
function loadActiveTab() {
    try {
        const saved = localStorage.getItem(TAB_STORAGE_KEY);
        if (VALID_TABS.includes(saved)) return saved;
    } catch (e) {}
    return 'status';
}
let activeTab = loadActiveTab();

// Branches-tab filter state (persisted across re-renders of the tab).
let branchSearch = '';
let branchCommitterFilter = '';

let gitData = {
    status: '',
    log: '',
    branches: '',
    stash: ''
};

function showError(msg) {
  const container = document.getElementById('toast-container');
  const toast = document.createElement('div');
  toast.className = 'toast';
  const text = document.createElement('span'); text.textContent = msg;
  const closeBtn = document.createElement('span'); closeBtn.className = 'close'; closeBtn.textContent = '×';
  closeBtn.onclick = () => toast.remove();
  toast.appendChild(text); toast.appendChild(closeBtn);
  container.appendChild(toast);
}

// A neutral toast with a one-click action (e.g. "元に戻す") for undoable
// operations. The action button dismisses the toast and runs onAction().
function showUndoToast(msg, actionLabel, onAction) {
  const container = document.getElementById('toast-container');
  const toast = document.createElement('div');
  toast.className = 'toast info';

  const text = document.createElement('span'); text.textContent = msg;

  const actions = document.createElement('div'); actions.className = 'toast-actions';
  const undoBtn = document.createElement('button'); undoBtn.className = 'toast-undo'; undoBtn.type = 'button'; undoBtn.textContent = actionLabel;
  undoBtn.onclick = () => { toast.remove(); onAction(); };
  const closeBtn = document.createElement('span'); closeBtn.className = 'close'; closeBtn.textContent = '×';
  closeBtn.onclick = () => toast.remove();
  actions.appendChild(undoBtn); actions.appendChild(closeBtn);

  toast.appendChild(text); toast.appendChild(actions);
  container.appendChild(toast);
  setTimeout(() => { if (toast.parentNode) toast.remove(); }, 6000);
}

function deepMerge(target, source) {
  for (const key of Object.keys(source)) {
    // settings API 由来のキーをマージするため、プロトタイプ汚染キーは無視する
    if (key === '__proto__' || key === 'constructor' || key === 'prototype') continue;
    if (source[key] !== null && typeof source[key] === 'object' && !Array.isArray(source[key]) && key in target) {
      target[key] = deepMerge(target[key], source[key]);
    } else {
      target[key] = source[key];
    }
  }
  return target;
}

async function loadSettings() {
  try {
    const saved = await fetch(`/api/settings/tool/${TOOL_ID}`).then(r => r.json());
    gitConfig = deepMerge(structuredClone(DEFAULT_CONFIG), saved);
  } catch (e) { console.error(e); }

  applyLayout();
  updateFooterHints();
}

async function saveSettings() {
  await fetch(`/api/settings/tool/${TOOL_ID}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(gitConfig),
  });
  applyLayout();
  updateFooterHints();
}

function applyLayout() {
  const leftPane = document.getElementById('left-pane');
  const logPanel = document.getElementById('log-panel');
  leftPane.style.width = `${gitConfig.layout.left_width}px`;

  if (gitConfig.layout.show_log) {
      logPanel.style.display = 'block';
      logPanel.style.height = `${gitConfig.layout.log_height}px`;
  } else {
      logPanel.style.display = 'none';
  }
}

async function fetchRepos() {
  try {
    const repos = await fetch('/api/repos').then(r => r.json());
    allRepos = applyRepoOrder(repos);

    // The settings "Default Repo" select lists every repo (incl. hidden ones) so
    // any can still be chosen as the default.
    const settingSelect = document.getElementById('setting-default-repo');
    settingSelect.innerHTML = '';
    const emptyOpt = document.createElement('option'); emptyOpt.value = ''; emptyOpt.textContent = 'Select Repo...';
    settingSelect.appendChild(emptyOpt);
    allRepos.forEach(r => {
      const opt = document.createElement('option'); opt.value = r.path; opt.textContent = r.name;
      settingSelect.appendChild(opt);
    });

    // Pick the current repo: the repo last viewed (persisted across reloads) if
    // still present and not hidden, then the configured default (if present and
    // not hidden), otherwise the first visible repo, otherwise the first / none.
    const visible = allRepos.filter(r => !isHidden(r.path));
    const savedRepo = loadSavedRepo();
    if (savedRepo
        && allRepos.find(r => r.path === savedRepo)
        && !isHidden(savedRepo)) {
      currentRepo = savedRepo;
    } else if (gitConfig.default_repo
        && allRepos.find(r => r.path === gitConfig.default_repo)
        && !isHidden(gitConfig.default_repo)) {
      currentRepo = gitConfig.default_repo;
    } else if (visible.length > 0) {
      currentRepo = visible[0].path;
    } else if (allRepos.length > 0) {
      currentRepo = allRepos[0].path;
    } else {
      currentRepo = '';
    }

    renderRepoSwitcher();
    renderEmptyState();
  } catch (e) {
    showError('Failed to fetch repositories.');
    console.error(e);
  }
}

function renderEmptyState() {
  const mainContent = document.getElementById('main-content');
  const existing = document.getElementById('empty-state');
  if (existing) existing.remove();

  const tabs = document.querySelector('.tabs');

  if (allRepos.length === 0) {
    document.getElementById('left-pane').style.display = 'none';
    document.getElementById('right-pane').style.display = 'none';
    document.getElementById('log-panel').style.display = 'none';
    if (tabs) tabs.style.display = 'none';

    const el = document.createElement('div');
    el.id = 'empty-state';
    el.className = 'empty-state';
    el.innerHTML = `
      <div class="empty-icon">⎇</div>
      <div class="empty-title">リポジトリが登録されていません</div>
      <div class="empty-desc">
        パスを指定するか、ディレクトリから探してリポジトリを追加するか、<br>
        <a href="/workspace" style="color:var(--accent);text-decoration:none;">workspace ツール</a>でスキャン対象フォルダを設定してください。
      </div>
      <button class="empty-btn" onclick="document.getElementById('btn-add-repo').click()">＋ リポジトリを追加</button>
    `;
    mainContent.style.position = 'relative';
    mainContent.appendChild(el);
  } else {
    document.getElementById('left-pane').style.display = 'flex';
    document.getElementById('right-pane').style.display = 'flex';
    if (tabs) tabs.style.display = 'flex';
    applyLayout();
  }
}

// Resizing logic
let isResizingX = false, isResizingY = false;
document.getElementById('resizer-x').addEventListener('mousedown', (e) => { isResizingX = true; });
document.getElementById('resizer-y').addEventListener('mousedown', (e) => { isResizingY = true; });
document.addEventListener('mousemove', (e) => {
  if (isResizingX) {
      const newWidth = e.clientX;
      if (newWidth > 150 && newWidth < window.innerWidth - 100) {
          gitConfig.layout.left_width = newWidth;
          applyLayout();
      }
  }
  if (isResizingY) {
      const footerHeight = 24;
      const newHeight = window.innerHeight - e.clientY - footerHeight;
      if (newHeight > 50 && newHeight < window.innerHeight - 200) {
          gitConfig.layout.log_height = newHeight;
          applyLayout();
      }
  }
});
document.addEventListener('mouseup', () => {
    if (isResizingX || isResizingY) {
        isResizingX = false; isResizingY = false;
        saveSettings();
    }
});


