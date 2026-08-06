// --- Scenario switching ---
// Component states and scenarios come from /api/envs/state, what a switch
// would do from /api/envs/switch/plan, and only an explicit confirmation of
// that plan reaches /api/envs/switch/apply. Nothing here stops or starts
// anything without the user having seen the list first.

let switchState = { environments: [] };
// Whether the last state fetch failed. switchState keeps the previous values on
// a failure (an empty section is worse than a stale one), so "we have values"
// and "the values are current" are different questions and need separate
// answers: the component rows say which one they are showing, and
// startComponent refuses to build a target out of the stale one.
let switchStateStale = false;
// The plan being confirmed, with the request that produced it: apply re-sends
// that same target plus the plan's fingerprint, so the user cannot approve one
// set of stops and have another one run.
let pendingSwitch = null;

// onClose に置くのが要点。閉じる経路は「閉じる」ボタンだけではなく Escape も
// あるので、pendingSwitch の破棄を close 関数の側に書くと Escape で確認済みの
// プランが残り、次に開いた画面と食い違ったまま適用できてしまう。
const switchModal = DevhubModal.attach('switchModalOverlay', {
  labelledBy: 'switchModalTitle',
  onClose: () => { pendingSwitch = null; },
});

// State is fetched on load and after an apply, never polled: probing a
// compose_service shells out to `docker compose ps`, so a background poll
// would spawn processes forever.
//
// Returns whether the values are now current. A failure is still non-fatal for
// the page — the section keeps rendering the previous values — but it must not
// look like a successful read to a caller that is about to compute something
// from them.
async function fetchSwitchState() {
  try {
    const res = await fetch('/api/envs/state');
    const data = await res.json();
    if (!res.ok || data.error) throw new Error(data.error || `HTTP ${res.status}`);
    switchState = data;
    switchStateStale = false;
  } catch (e) {
    switchStateStale = true;
  }
  render();
  return !switchStateStale;
}

function switchEnvState(envId) {
  return (switchState.environments || []).find(e => e.id === envId);
}

// --- plan ---

async function requestSwitchPlan(envId, target, title) {
  try {
    const res = await fetch('/api/envs/switch/plan', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(Object.assign({env_id: envId}, target))
    });
    const plan = await res.json();
    if (!res.ok || plan.error) throw new Error(plan.error || '切替内容を計算できませんでした');
    pendingSwitch = {envId, target, plan};
    showSwitchPlan(plan, title);
  } catch (e) {
    alert('切替内容を計算できませんでした: ' + e.message);
  }
}

// The scenario's display name is passed through for the dialog title: the plan
// itself only carries ids, and an id is not what the user clicked.
function switchToScenario(envId, scenarioId, scenarioName) {
  requestSwitchPlan(envId, {scenario_id: scenarioId}, `シナリオ「${scenarioName || scenarioId}」へ切り替え`);
}

// An empty selection means "keep only the shared components" — the 全停止
// action, which still leaves shared infrastructure (a database) running.
function stopScenarioComponents(envId) {
  requestSwitchPlan(envId, {components: []}, 'シナリオのコンポーネントを全停止');
}

// Starting one component. The target is declarative, so sending {components:
// [id]} alone would mean "and stop every other scenario component" — the
// opposite of what a per-row 起動 button promises. The additive meaning is
// expressed by targeting what is already running, plus the requested one.
//
// State is re-read first rather than trusted from the last fetch: a target
// built from a stale snapshot would list something started since as a stop.
// The plan screen would still show it, but a 起動 button should not be
// producing stops to review in the first place. That only holds if the re-read
// actually succeeded, so a failed one aborts rather than falling back to the
// values it was meant to replace — the fingerprint does not catch this, since
// it validates the plan against the same current state that the wrong target
// was derived against.
async function startComponent(envId, componentId, label) {
  if (!await fetchSwitchState()) {
    alert('コンポーネントの状態を取得できませんでした。今動いているものが分からないまま起動すると、別のコンポーネントを停止対象にしてしまいます。');
    return;
  }
  const state = switchEnvState(envId);
  if (!state) {
    alert('コンポーネントの状態を取得できないため、起動内容を計算できません。');
    return;
  }
  // Unknown components are left out of the ids listed here: they are not
  // stopped either way (planSwitch warns instead of stopping them), and listing
  // one would start a duplicate of something that may already be running.
  //
  // This covers only what is listed. targetComponents (switch.go) re-adds each
  // target's depends_on transitively, so an unknown component that a running
  // one depends on still enters the target and is planned as a start. That is
  // the server's rule for every switch, not something this button can opt out
  // of; the plan carries planSwitch's duplication warning for it, which is what
  // the confirmation screen is for.
  const running = (state.components || []).filter(c => c.state === 'running').map(c => c.id);
  const target = running.includes(componentId) ? running : running.concat([componentId]);
  requestSwitchPlan(envId, {components: target}, `「${label || componentId}」を起動`);
}

function planSteps(title, steps, cls) {
  if (!steps || !steps.length) return '';
  const items = steps.map(s =>
    `<li class="${cls}">${escapeHtml(s.label)}<span class="plan-kind">${escapeHtml(s.kind)}</span></li>`).join('');
  return `<div class="plan-section"><div class="plan-title">${title}</div><ul class="plan-list">${items}</ul></div>`;
}

function showSwitchPlan(plan, title) {
  const stops = plan.stop || [];
  const starts = plan.start || [];
  document.getElementById('switchModalTitle').textContent = title || '切替内容の確認';

  let html = planSteps('停止', stops, 'plan-stop')
    + planSteps('維持', plan.keep, 'plan-keep')
    + planSteps('起動', starts, 'plan-start');
  if (!stops.length && !starts.length) {
    html += '<div class="empty" style="padding:16px;">変更はありません。</div>';
  }
  if ((plan.warnings || []).length) {
    html += `<div class="plan-section plan-warnings"><div class="plan-title">警告</div><ul class="plan-list">${
      plan.warnings.map(w => `<li>${escapeHtml(w)}</li>`).join('')}</ul></div>`;
  }
  document.getElementById('switchModalBody').innerHTML = html;

  // Stopping is the destructive half, so the button says how much of it there
  // is instead of a generic 適用.
  const apply = document.getElementById('switchApplyBtn');
  apply.style.display = '';
  apply.disabled = !stops.length && !starts.length;
  apply.textContent = stops.length ? `${stops.length}件を停止して切り替える` : '起動する';
  apply.className = stops.length ? 'btn btn-danger' : 'btn btn-success';
  switchModal.open();
}

function closeSwitchModal() {
  switchModal.close();
}

// --- apply ---

async function applySwitchPlan() {
  if (!pendingSwitch) return;
  const {envId, target, plan} = pendingSwitch;
  const apply = document.getElementById('switchApplyBtn');
  const label = apply.textContent;
  apply.disabled = true;
  apply.textContent = '適用中...';
  try {
    const res = await fetch('/api/envs/switch/apply', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(Object.assign({env_id: envId, fingerprint: plan.fingerprint}, target))
    });
    const result = await res.json();
    if (!res.ok || result.error) throw new Error(result.error || '適用に失敗しました');
    showSwitchResults(result);
  } catch (e) {
    alert(e.message);
    apply.disabled = false;
    apply.textContent = label;
  }
  // Whatever happened, what is running has probably changed.
  fetchSwitchState();
  fetchLaunches();
}

function showSwitchResults(result) {
  const rows = (result.results || []).map(r => {
    const action = r.action === 'stop' ? '停止' : '起動';
    const detail = r.error ? `<span class="plan-error">${escapeHtml(r.error)}</span>` : '';
    return `<li class="${r.ok ? 'plan-ok' : 'plan-fail'}">${r.ok ? '✔' : '✖'} ${action}: ${escapeHtml(r.label)}${detail}</li>`;
  }).join('');
  document.getElementById('switchModalBody').innerHTML =
    `<div class="plan-section"><div class="plan-title">${result.ok ? '適用しました' : '一部の操作が失敗しました'}</div>` +
    `<ul class="plan-list">${rows}</ul></div>`;
  document.getElementById('switchApplyBtn').style.display = 'none';
  pendingSwitch = null;
}
