/* shared/net.js — the JSON-over-fetch contract shared by devhub tool pages.
   The API token is attached globally by the injected fetch shim (see
   internal/server/inject.go), so these helpers own only the JSON envelope:
   decode the body and surface {error} as a thrown Error, so every caller can
   try/catch uniformly. Loaded via <script src="/shared/net.js"> in <head>.
   Contract tier of shared/ — one definition, previously copy-pasted
   byte-for-byte into db-table and ports. */

// apiJson performs a fetch and returns the parsed JSON body, throwing
// Error(data.error) on a non-2xx response.
async function apiJson(url, options = {}) {
  const res = await fetch(url, options);
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || 'request failed');
  return data;
}

// postJson POSTs a JSON body and returns the parsed response via apiJson.
async function postJson(url, body) {
  return apiJson(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}
