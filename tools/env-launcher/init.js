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
  // v2 components reuse the process modal — a host_process component is a
  // process plus kind/lifecycle — so the same opener serves both.
  else if (action === 'add-component') openProcessModal(Number(btn.dataset.eIdx), -1);
  else if (action === 'edit-component') openProcessModal(Number(btn.dataset.eIdx), Number(btn.dataset.cIdx));
  else if (action === 'delete-component') deleteComponent(Number(btn.dataset.eIdx), Number(btn.dataset.cIdx));
  else if (action === 'add-scenario') openScenarioModal(Number(btn.dataset.eIdx), -1);
  else if (action === 'edit-scenario') openScenarioModal(Number(btn.dataset.eIdx), Number(btn.dataset.sIdx));
  else if (action === 'delete-scenario') deleteScenario(Number(btn.dataset.eIdx), Number(btn.dataset.sIdx));
  else if (action === 'switch-scenario') switchToScenario(btn.dataset.envId, btn.dataset.scenarioId, btn.dataset.scenarioName);
  else if (action === 'switch-stop') stopScenarioComponents(btn.dataset.envId);
  else if (action === 'edit-runtime') openRuntimeModal(Number(btn.dataset.eIdx));
});

// Init
fetchWorktrees();
fetchEnvs();
fetchLaunches();
// Component state and host capabilities are fetched once, not polled: both
// shell out to container CLIs (see switch.js).
fetchSwitchState();
fetchRuntimes();
setInterval(fetchLaunches, 5000);
