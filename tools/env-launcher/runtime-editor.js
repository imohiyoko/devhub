// --- Runtime (execution base) ---
// What /api/envs/runtimes says this host can offer, and the editor that writes
// an environment's chosen provider/profile/engine back into the document.
//
// The whole point of reading capabilities from the server is that this file
// hardcodes no provider or engine names (plan §6.4): the selects are built from
// what the host actually reports, so an option the user can pick is always one
// devhub can act on.

let runtimeCaps = { providers: [] };
// The environment index being edited, so save() can merge into the right entry.
let currentRuntimeIndex = -1;

// Capabilities are fetched once on load, like component state and for the same
// reason: the probe shells out to `docker compose version` and `colima list`.
async function fetchRuntimes() {
  try {
    const res = await fetch('/api/envs/runtimes');
    const data = await res.json();
    if (res.ok && !data.error) {
      runtimeCaps = data;
      render();
    }
  } catch (e) { /* non-fatal: the runtime section falls back to the raw values */ }
}

function runtimeProvider(id) {
  return (runtimeCaps.providers || []).find(p => p.id === id);
}

// selectableProviders drops the ones this OS can never offer. A Linux user is
// not helped by a greyed-out Colima row; a macOS user without Colima is, so
// that one stays visible with its reason (plan §10).
function selectableProviders() {
  return (runtimeCaps.providers || []).filter(p => p.supported);
}

// runtimeLabel is how an environment's declared runtime reads on its card.
function runtimeLabel(rt) {
  if (!rt || !rt.provider) return '';
  const provider = runtimeProvider(rt.provider);
  const parts = [provider ? provider.label : rt.provider];
  if (rt.provider === 'colima') {
    parts.push(rt.profile || 'default');
    // The engine is optional in the document; the profile's own engine is
    // shown instead when it is known, and neither is invented.
    const profile = (provider && (provider.profiles || []).find(p => p.name === (rt.profile || 'default'))) || null;
    const engine = rt.engine || (profile && profile.engine) || '';
    if (engine) parts.push(engine);
  }
  return parts.join(' / ');
}

// runtimeSectionHtml renders the current execution base on an environment card,
// with whatever the host says is wrong with it.
function runtimeSectionHtml(env, eIdx) {
  const rt = (switchEnvState(env.id) || {}).runtime || env.runtime || {};
  const provider = runtimeProvider(rt.provider);
  const notes = [];
  if (provider && !provider.available && provider.reason) {
    notes.push(provider.reason);
  }
  if (rt.provider === 'colima' && provider && provider.available) {
    const name = rt.profile || 'default';
    const profile = (provider.profiles || []).find(p => p.name === name);
    if (!profile) {
      notes.push(`profile '${name}' がありません`);
    } else {
      if (profile.status && profile.status.toLowerCase() !== 'running') notes.push(`profile は ${profile.status}`);
      if (!profile.supported && profile.reason) notes.push(profile.reason);
    }
  }
  const warn = notes.length
    ? `<div class="runtime-note">${escapeHtml(notes.join(' / '))}</div>`
    : '';
  return `
    <div class="runtime-section">
      <div class="runtime-head">
        <span class="runtime-label">実行基盤: ${escapeHtml(runtimeLabel(rt) || '未設定')}</span>
        <button class="btn btn-sm" data-action="edit-runtime" data-e-idx="${eIdx}">変更</button>
      </div>
      ${warn}
    </div>`;
}

// --- editor ---

// Escape・Tab の閉じ込め・フォーカス復帰・role/aria は shared/modal.js が持つ。
const runtimeModal = DevhubModal.attach('runtimeModalOverlay', { labelledBy: 'runtimeModalTitle' });

async function openRuntimeModal(index) {
  // Capabilities are fetched on load in parallel with everything else, so a
  // quick click can arrive first. Opening the picker with no options would let
  // the user "save" a provider that was never offered, so the modal waits for
  // real data — the same await-then-open shape openEnvModal uses for
  // worktrees. The host always reports three providers, so an empty list can
  // only mean the request has not succeeded.
  if (!(runtimeCaps.providers || []).length) await fetchRuntimes();
  if (!(runtimeCaps.providers || []).length) {
    alert('実行基盤を取得できませんでした。しばらくしてからもう一度お試しください。');
    return;
  }

  currentRuntimeIndex = index;
  const rt = (envsData.environments[index] || {}).runtime || {};
  // The document's provider, defaulted once: an environment that declares no
  // runtime is on the docker provider, and every branch below must agree.
  const want = rt.provider || 'docker';
  const select = document.getElementById('runtimeProvider');
  select.innerHTML = selectableProviders().map(p => {
    const suffix = p.available ? '' : '（利用不可）';
    return `<option value="${escapeHtml(p.id)}">${escapeHtml(p.label)}${suffix}</option>`;
  }).join('');
  select.value = want;
  // A document naming a provider this host cannot offer still has to be
  // editable, so its value is kept as an option rather than silently reset.
  if (select.value !== want) {
    select.insertAdjacentHTML('beforeend', `<option value="${escapeHtml(want)}">${escapeHtml(want)}（この環境では利用不可）</option>`);
    select.value = want;
  }

  // The stored values are passed in rather than assigned to the selects first:
  // the option lists do not exist until syncRuntimeFields builds them, and
  // assigning to an empty <select> silently keeps the empty value.
  syncRuntimeFields(rt.profile || '', rt.engine || '');
  runtimeModal.open();
}

function closeRuntimeModal() {
  runtimeModal.close();
}

// syncRuntimeFields rebuilds the profile/engine selects from the host's
// capabilities, keeping the values passed in (or, when called from the
// provider's onchange, whatever is currently selected).
//
// The fields are shown only for the provider that honours them: the server
// rejects a profile or engine on any other provider, so offering them there
// would be offering a save that cannot succeed.
function syncRuntimeFields(wantProfile, wantEngine) {
  const provider = document.getElementById('runtimeProvider').value;
  const isColima = provider === 'colima';
  document.getElementById('runtimeColimaFields').style.display = isColima ? 'block' : 'none';

  const caps = runtimeProvider(provider) || {};
  const profileSel = document.getElementById('runtimeProfile');
  const current = wantProfile !== undefined ? wantProfile : profileSel.value;
  const profiles = caps.profiles || [];
  profileSel.innerHTML = '<option value="">(default)</option>' + profiles.map(p => {
    const bits = [p.status, p.engine || 'engine 不明', p.supported ? '' : '未対応'].filter(Boolean);
    return `<option value="${escapeHtml(p.name)}">${escapeHtml(p.name)} — ${escapeHtml(bits.join(' / '))}</option>`;
  }).join('');
  profileSel.value = current;
  // A profile the document names but this host does not have must survive an
  // edit of some other field, so it is added rather than dropped.
  if (current && profileSel.value !== current) {
    profileSel.insertAdjacentHTML('beforeend', `<option value="${escapeHtml(current)}">${escapeHtml(current)} — 未検出</option>`);
    profileSel.value = current;
  }

  const engineSel = document.getElementById('runtimeEngine');
  const engine = wantEngine !== undefined ? wantEngine : engineSel.value;
  engineSel.innerHTML = '<option value="">(profile に従う)</option>' +
    (caps.engines || []).map(e => `<option value="${escapeHtml(e)}">${escapeHtml(e)}</option>`).join('');
  engineSel.value = engine;
  if (engine && engineSel.value !== engine) {
    engineSel.insertAdjacentHTML('beforeend', `<option value="${escapeHtml(engine)}">${escapeHtml(engine)}（このホストでは駆動できません）</option>`);
    engineSel.value = engine;
  }
}

async function saveRuntime() {
  const provider = document.getElementById('runtimeProvider').value;
  const runtime = { provider: provider };
  // profile and engine are only sent for colima: the server treats a field it
  // does not honour as an error rather than ignoring it, and rightly so.
  if (provider === 'colima') {
    const profile = document.getElementById('runtimeProfile').value.trim();
    const engine = document.getElementById('runtimeEngine').value.trim();
    if (profile) runtime.profile = profile;
    if (engine) runtime.engine = engine;
  }

  const at = currentRuntimeIndex;
  if (!envsData.environments[at]) return;
  // saveEnvDocEdit puts the environment back if the save is rejected. A
  // rejected runtime left in envsData would be persisted by the next save of
  // anything else, which the user would have no reason to connect to the
  // failure they already dismissed. It also re-reads the observed state, which
  // this edit invalidates: the declared runtime decides which engine the
  // components are probed on.
  const saved = await saveEnvDocEdit(() => {
    envsData.environments[at] = Object.assign({}, envsData.environments[at], { runtime: runtime });
  });
  // Only close once the save landed, so the user can correct a rejected value.
  if (saved) closeRuntimeModal();
}
