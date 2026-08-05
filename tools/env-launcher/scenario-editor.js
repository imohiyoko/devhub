// --- v2 scenario editor ---
//
// A scenario names the set of scenario-lifecycle components a switch turns on.
// Shared components are not listed: targetComponents adds them to whichever
// target is chosen, so offering them here would suggest a choice that has no
// effect. An id listed in the stored config that is shared is dropped on save
// for the same reason — it never meant anything.
//
// Unlike components, scenarios have no default: decodeEnvironment synthesises
// one only for v1 documents, so a v2 environment with no scenarios really has
// none, and nothing can be switched to.

let currentScenarioIndex = -1;

// Mirrors envIDRe on the server, which rejects a scenario id outright.
const scenarioIDRe = /^[a-zA-Z0-9_-]+$/;

// Escape・Tab の閉じ込め・フォーカス復帰・role/aria は shared/modal.js が持つ。
const scenarioModal = DevhubModal.attach('scenarioModalOverlay', { labelledBy: 'scenarioModalTitle' });

function openScenarioModal(envIndex, scenarioIndex = -1) {
  currentEnvIndex = envIndex;
  currentScenarioIndex = scenarioIndex;
  document.getElementById('scenarioModalTitle').textContent = scenarioIndex >= 0 ? 'シナリオの編集' : 'シナリオの追加';

  const env = envsData.environments[envIndex];
  const scenario = scenarioIndex >= 0 ? (scenariosOf(env)[scenarioIndex] || {}) : {};
  const members = scenario.components || [];
  document.getElementById('scenarioId').value = scenario.id || '';
  document.getElementById('scenarioName').value = scenario.name || '';

  // Build the options with their selection already set: assigning .value or
  // .selected to a <select> whose options do not exist yet is a silent no-op.
  document.getElementById('scenarioComponents').innerHTML = componentsOf(env)
    .filter(c => c.lifecycle !== 'shared')
    .map(c => `<option value="${escapeHtml(c.id)}"${members.includes(c.id) ? ' selected' : ''}>${escapeHtml(c.label || c.id)}</option>`)
    .join('');

  scenarioModal.open();
}

function closeScenarioModal() {
  scenarioModal.close();
}

async function saveScenario() {
  const id = document.getElementById('scenarioId').value.trim();
  if (!scenarioIDRe.test(id)) {
    alert('シナリオIDを英数字・_・- で入力してください。');
    return;
  }
  // Snapshot the indices: the save is awaited and the modal stays open on
  // failure, so nothing should depend on the globals still pointing here.
  const envIndex = currentEnvIndex;
  const at = currentScenarioIndex;
  const env = envsData.environments[envIndex];
  if (scenariosOf(env).some((s, i) => s.id === id && i !== at)) {
    alert('このシナリオIDは既に環境内に存在します。');
    return;
  }

  const scenario = {
    id: id,
    name: document.getElementById('scenarioName').value.trim(),
    components: Array.from(document.getElementById('scenarioComponents').selectedOptions).map(o => o.value),
  };
  const saved = await saveEnvEdit(envIndex, e => {
    if (!Array.isArray(e.scenarios)) e.scenarios = [];
    if (at >= 0) e.scenarios[at] = scenario;
    else e.scenarios.push(scenario);
  });
  if (saved) closeScenarioModal();
}

function deleteScenario(envIndex, scenarioIndex) {
  const env = envsData.environments[envIndex];
  const scenario = scenariosOf(env)[scenarioIndex];
  if (!scenario) return;
  // Losing the last scenario leaves the environment with nothing to switch to
  // — the shared components keep running, but no scenario component can start.
  const note = scenariosOf(env).length === 1
    ? '\nこれが最後のシナリオです。削除すると切り替え先がなくなります。'
    : '';
  if (!confirm(`シナリオ「${scenario.name || scenario.id}」を削除しますか？${note}`)) return;
  saveEnvEdit(envIndex, e => {
    if (Array.isArray(e.scenarios)) e.scenarios.splice(scenarioIndex, 1);
  });
}
