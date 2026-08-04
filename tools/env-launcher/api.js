// --- API ---
async function fetchEnvs() {
  try {
    const res = await fetch('/api/envs');
    const data = await res.json();
    if (!res.ok || data.error) throw new Error(data.error || 'Failed to fetch environments');
    envsData = data;
    envsLoaded = true;
    render();
  } catch(e) {
    // Leave envsLoaded false so saves stay blocked until a reload succeeds.
    alert('環境の読み込みに失敗しました。保存は無効化されています。ページを再読み込みしてください: ' + e.message);
  }
}

// Only one save may be in flight at a time. Every editor mutates envsData first
// and persists after, and the re-render that would refresh the indices in the
// DOM only happens once the save returns — so a second save starting inside
// that window acts on positions the first has already shifted, and can delete
// the wrong row. A rollback is just as bad: its snapshot predates the second
// edit, so restoring it discards that edit silently. The window is only a local
// POST, but the failure is invisible, so it is closed rather than raced.
let envSaveInFlight = false;

// saveEnvsData persists the whole document and reports whether it landed. The
// caller needs that answer: this is a full-document replace, so an edit left in
// envsData after a failed save is not discarded — it rides along with whatever
// is saved next, long after the alert the user dismissed.
async function saveEnvsData() {
  // Never persist before a successful load, or we'd replace the stored
  // document with the empty/partial default and lose every environment.
  if (!envsLoaded) {
    alert('環境がまだ読み込まれていないため保存できません。ページを再読み込みしてください。');
    return false;
  }
  if (envSaveInFlight) {
    alert('前の保存がまだ完了していません。少し待ってからもう一度お試しください。');
    return false;
  }
  envSaveInFlight = true;
  try {
    const res = await fetch('/api/envs', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(envsData)
    });
    const data = await res.json();
    if (!res.ok || data.error) throw new Error(data.error || 'Failed to save environments');
    render();
    return true;
  } catch(e) {
    alert('Error saving environments: ' + e.message);
    return false;
  } finally {
    envSaveInFlight = false;
  }
}

// saveEnvDocEdit applies a mutation to the stored document and persists it,
// restoring the document when the save does not land. Every path that changes
// envsData goes through here.
//
// The restore is what makes a rejected save harmless. saveEnvsData replaces the
// whole document, so an edit left in envsData after a rejection is not
// discarded: render() would draw it as though it had been applied, and the next
// save the user made for an unrelated reason would persist it. That is true of
// a server-side validation error and equally of a save refused for arriving
// while another one was still in flight.
//
// A successful save re-reads the observed state for v2 documents: cards render
// their component list from the config, but the state dots beside it come from
// /api/envs/state, which a newly added or removed component is not in yet. v1
// cards show no state and the probe costs a port scan, so they do not pay for it.
async function saveEnvDocEdit(mutate) {
  const before = JSON.parse(JSON.stringify(envsData));
  mutate();
  if (await saveEnvsData()) {
    if (isV2Document()) fetchSwitchState();
    return true;
  }
  envsData = before;
  render();
  return false;
}

// saveEnvEdit is saveEnvDocEdit scoped to one environment, which is all the
// component and process editors ever change.
function saveEnvEdit(envIndex, mutate) {
  return saveEnvDocEdit(() => mutate(envsData.environments[envIndex]));
}

async function launchEnv(envId) {
  try {
    const res = await fetch('/api/envs/launch', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({env_id: envId})
    });
    const result = await res.json();
    if (result.error) alert('Error: ' + result.error);
    else {
      showToast('起動を開始しました。ターミナルとエラーログを確認してください。');
      setTimeout(fetchLaunches, 1000);
    }
  } catch(e) {
    alert('Failed to launch environment');
  }
}

async function launchProcess(envId, procId) {
  try {
    const res = await fetch('/api/envs/launch/process', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({env_id: envId, process_id: procId})
    });
    const result = await res.json();
    if (result.error) alert('Error: ' + result.error);
    else showToast('プロセス起動を開始しました。ターミナルを確認してください。');
  } catch(e) {
    alert('Failed to launch process');
  }
}
