// --- v2 component editor ---
//
// A v2 environment's units are components, not processes, and the server
// rejects a v2 document that carries a `processes` key at all. The two shapes
// are close enough to share one modal: a host_process component *is* a process
// object plus kind/lifecycle (decodeComponent reads decodeProcess off the
// component's own top level, not off a nested object). So process-editor.js
// does the field work for both, and this file supplies the v2-only parts — the
// kind toggle, the compose payload, and deletion, which has to reach into
// scenarios.

function componentsOf(env) {
  return Array.isArray(env && env.components) ? env.components : [];
}

function scenariosOf(env) {
  return Array.isArray(env && env.scenarios) ? env.scenarios : [];
}

// syncComponentKind shows the field group the selected kind actually uses. The
// other group stays in the DOM but is never read on save, so a compose_service
// cannot carry a stray command left over from a kind switch.
function syncComponentKind() {
  const compose = document.getElementById('procKind').value === 'compose_service';
  document.getElementById('procHostFields').style.display = compose ? 'none' : '';
  document.getElementById('procComposeFields').style.display = compose ? '' : 'none';
}

// textareaLines splits a textarea into trimmed, non-empty lines — the shape the
// backend wants for compose files and services (arrays of strings).
function textareaLines(id) {
  return document.getElementById(id).value.split('\n').map(s => s.trim()).filter(Boolean);
}

// fillComponentFields populates the v2-only inputs. Safe to call for v1 too:
// the group is hidden and saveProcess never reads it.
function fillComponentFields(comp) {
  document.getElementById('procKind').value = comp.kind === 'compose_service' ? 'compose_service' : 'host_process';
  document.getElementById('procLifecycle').value = comp.lifecycle === 'shared' ? 'shared' : 'scenario';
  const compose = comp.compose || {};
  document.getElementById('procComposeCwd').value = compose.cwd || '';
  document.getElementById('procComposeProject').value = compose.project || '';
  document.getElementById('procComposeServices').value = (compose.services || []).join('\n');
  document.getElementById('procComposeFiles').value = (compose.files || []).join('\n');
  syncComponentKind();
}

// readComposeFields mirrors validateCompose on the server (cwd, project and at
// least one service are required) so the user is told which field is missing,
// instead of having the whole document rejected with one message.
function readComposeFields() {
  const cwd = document.getElementById('procComposeCwd').value.trim();
  const project = document.getElementById('procComposeProject').value.trim();
  const services = textareaLines('procComposeServices');
  if (!cwd) { alert('compose_service には作業ディレクトリ (CWD) が必要です。'); return null; }
  if (!project) { alert('compose_service には project 名が必要です。'); return null; }
  if (!services.length) { alert('compose_service にはサービスを1つ以上指定してください。'); return null; }
  const compose = { cwd: cwd, project: project, services: services };
  const files = textareaLines('procComposeFiles');
  if (files.length) compose.files = files;
  return compose;
}

// deleteComponent also drops the id from every scenario that lists it. A
// scenario referencing a component that no longer exists fails validateScenarios
// on the server, which rejects the entire document — including untouched edits
// to other environments.
function deleteComponent(envIndex, compIndex) {
  const env = envsData.environments[envIndex];
  const comp = componentsOf(env)[compIndex];
  if (!comp) return;
  const usedBy = scenariosOf(env).filter(s => (s.components || []).includes(comp.id));
  const note = usedBy.length
    ? `\nシナリオ「${usedBy.map(s => s.name || s.id).join('」「')}」からも取り除かれます。`
    : '';
  if (!confirm(`コンポーネント「${comp.label || comp.id}」を削除しますか？${note}`)) return;
  saveEnvEdit(envIndex, e => {
    e.components.splice(compIndex, 1);
    scenariosOf(e).forEach(s => {
      if (Array.isArray(s.components)) s.components = s.components.filter(id => id !== comp.id);
    });
  });
}
