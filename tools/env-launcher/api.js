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

async function saveEnvsData() {
  // Never persist before a successful load, or we'd replace the stored
  // document with the empty/partial default and lose every environment.
  if (!envsLoaded) {
    alert('環境がまだ読み込まれていないため保存できません。ページを再読み込みしてください。');
    return;
  }
  try {
    const res = await fetch('/api/envs', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(envsData)
    });
    const data = await res.json();
    if (!res.ok || data.error) throw new Error(data.error || 'Failed to save environments');
    render();
  } catch(e) {
    alert('Error saving environments: ' + e.message);
  }
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
