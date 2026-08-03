// --- Env Modal ---
const envWtEnabled = document.getElementById('envWtEnabled');
envWtEnabled.addEventListener('change', () => {
  document.getElementById('envWtFields').style.display = envWtEnabled.checked ? 'block' : 'none';
});
// Repopulate branches when the env repo changes.
document.getElementById('envWtRepo').addEventListener('change', e => {
  fillBranchSelect(document.getElementById('envWtBranch'), e.target.value, '', '(branch を選択)');
});
// The env's declared repo scope (selected paths in the multi-select).
function selectedEnvRepos() {
  return Array.from(document.getElementById('envRepos').selectedOptions).map(o => o.value);
}
// Re-scope the env worktree repo picker when the allowed repos change.
document.getElementById('envRepos').addEventListener('change', () => {
  const repoSel = document.getElementById('envWtRepo');
  fillRepoSelect(repoSel, repoSel.value, '(repo を選択)', selectedEnvRepos());
  fillBranchSelect(document.getElementById('envWtBranch'), repoSel.value, document.getElementById('envWtBranch').value, '(branch を選択)');
});
// Same for the per-process binding repo picker.
document.getElementById('procBindRepo').addEventListener('change', e => {
  fillBranchSelect(document.getElementById('procBindBranch'), e.target.value, '', '(branch を選択)');
});

async function openEnvModal(index = -1) {
  currentEnvIndex = index;
  document.getElementById('envModalTitle').textContent = index >= 0 ? '環境の編集' : '環境の追加';
  await fetchWorktrees();

  const env0 = index >= 0 ? envsData.environments[index] : {};
  const wt = env0.worktree || {};
  if (index >= 0) {
    document.getElementById('envId').value = env0.id || '';
    document.getElementById('envName').value = env0.name || '';
    document.getElementById('envWtEnabled').checked = !!wt.enabled;
  } else {
    document.getElementById('envId').value = '';
    document.getElementById('envName').value = '';
    document.getElementById('envWtEnabled').checked = false;
  }
  fillReposMulti(document.getElementById('envRepos'), env0.repos || []);
  document.getElementById('envIps').value = (env0.ips || []).join('\n');
  const allowed = env0.repos || [];
  fillRepoSelect(document.getElementById('envWtRepo'), wt.repo_path || '', '(repo を選択)', allowed);
  fillBranchSelect(document.getElementById('envWtBranch'), wt.repo_path || '', wt.branch || '', '(branch を選択)');
  envWtEnabled.dispatchEvent(new Event('change'));
  document.getElementById('envModalOverlay').classList.add('open');
}

function closeEnvModal() {
  document.getElementById('envModalOverlay').classList.remove('open');
}

function saveEnv() {
  const id = document.getElementById('envId').value.trim();
  if (!id) {
    alert("IDを入力してください。");
    return;
  }

  if (currentEnvIndex === -1 && envsData.environments.some(e => e.id === id)) {
    alert("この環境IDは既に存在します。");
    return;
  }

  const repos = selectedEnvRepos();
  const ips = document.getElementById('envIps').value
    .split('\n').map(s => s.trim()).filter(Boolean);

  const wtRepo = document.getElementById('envWtRepo').value.trim();
  if (repos.length && wtRepo && !repos.map(expandHome).includes(expandHome(wtRepo))) {
    alert('Worktree の Repository が「許可する Repository」に含まれていません。スコープに追加するか選び直してください。');
    return;
  }

  const env = {
    id: id,
    name: document.getElementById('envName').value.trim(),
    repos: repos,
    ips: ips,
    worktree: {
      enabled: document.getElementById('envWtEnabled').checked,
      repo_path: wtRepo,
      branch: document.getElementById('envWtBranch').value.trim()
    }
  };

  if (!env.id) { alert('IDは必須です'); return; }

  if (currentEnvIndex >= 0) {
    env.processes = envsData.environments[currentEnvIndex].processes || [];
    envsData.environments[currentEnvIndex] = env;
  } else {
    env.processes = [];
    envsData.environments.push(env);
  }

  closeEnvModal();
  saveEnvsData();
}

function deleteEnv(index) {
  if (confirm('この環境を削除しますか？')) {
    envsData.environments.splice(index, 1);
    saveEnvsData();
  }
}
