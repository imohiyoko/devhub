// Events
document.getElementById('app').addEventListener('click', e => {
  const btn = e.target.closest('[data-action]');
  if (!btn) return;

  const action = btn.dataset.action;
  if (action === 'launch-env') launchEnv(btn.dataset.envId);
  else if (action === 'edit-env') openEnvModal(Number(btn.dataset.eIdx));
  else if (action === 'delete-env') deleteEnv(Number(btn.dataset.eIdx));
  else if (action === 'add-process') openProcessModal(Number(btn.dataset.eIdx), -1);
  else if (action === 'launch-process') launchProcess(btn.dataset.envId, btn.dataset.procId);
  else if (action === 'edit-process') openProcessModal(Number(btn.dataset.eIdx), Number(btn.dataset.pIdx));
  else if (action === 'delete-process') deleteProcess(Number(btn.dataset.eIdx), Number(btn.dataset.pIdx));
});

// Init
fetchWorktrees();
fetchEnvs();
fetchLaunches();
setInterval(fetchLaunches, 5000);
