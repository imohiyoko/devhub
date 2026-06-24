// Cross-tab rebuild reload (shared module: /shared/rebuild-reload.js)
//
// The dashboard's rebuild button only reloads the tab it was clicked in. When
// the server is rebuilt+restarted, every other open devhub tab (a tool page in
// another tab, etc.) would otherwise keep serving the stale, pre-rebuild UI
// until manually reloaded.
//
// The dashboard's doRebuild() writes localStorage key "devhub:reloaded" once
// the new server is confirmed UP. The storage event fires in every *other*
// same-origin tab (never the writer), so each of them reloads itself here. No
// polling, no server involvement — one signal fans out to all tabs.
addEventListener('storage', (e) => {
  if (e.key === 'devhub:reloaded') location.reload();
});
