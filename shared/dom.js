/* shared/dom.js — the DOM / HTML-safety primitives every devhub tool page shares.
   Loaded via <script src="/shared/dom.js"> in <head>, so every page gets the SAME
   escapeHtml. Previously this was copy-pasted into 7 pages and the copies had
   drifted (some to a weaker 3-character escape that missed quotes). This is the
   "contract" tier of shared/: a divergent copy here is an escaping bug, not a
   style choice, so there is exactly one definition. Tool-specific rendering stays
   in each page; only these cross-cutting primitives live here. */

// escapeHtml renders an arbitrary value as text-safe HTML, escaping the five
// characters that can break out of element text or a quoted attribute
// (& < > " '). null/undefined collapse to "" (never the literal "null").
function escapeHtml(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

// $ is document.getElementById — the single most repeated call across the pages.
// Provided for adoption; existing pages keep their explicit calls until touched.
function $(id) { return document.getElementById(id); }
