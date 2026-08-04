/* shared/modal.js — the focus and keyboard contract every devhub modal shares.

   A dialog that only *looks* modal is a dead end for anyone not driving with a
   mouse: Tab walks straight out of it into the page underneath (which is
   covered by the backdrop, so you are now typing into something you cannot
   see), Escape does nothing, and closing leaves focus at the top of the
   document instead of on the row you opened it from. devhub had all three, in
   different combinations per page — containers restored focus for its logs
   dialog and not its profile one, env-launcher's five modals had none of it,
   and no page trapped Tab at all. Fixing that per page would have produced
   eight slightly different answers to the same question.

   So this is the "contract" tier of shared/, next to dom.js and net.js: one
   definition of what happens when a devhub modal opens and closes. Pages keep
   owning what goes *inside* the dialog and when it opens; they hand this the
   overlay and get the keyboard behaviour.

   Usage — attach once at load, then open/close instead of toggling the class:

     const logs = DevhubModal.attach('logsOverlay', { labelledBy: 'logsTitle' });
     logs.open();    // shows it, moves focus in, remembers where focus came from
     logs.close();   // hides it, puts focus back where it was

   Visibility is the `open` class on the overlay, which is what every devhub
   overlay's CSS already keys off. Backdrop clicks stay with the page: they are
   a mouse affordance each page decides for itself, and routing them through the
   page's own close function (which calls close() here) keeps focus return and
   the canClose guard without this module having an opinion. */
(function () {
  'use strict';

  // What the browser will land on with Tab. Negative tabindex is excluded
  // because that is precisely the "focusable but not tabbable" marker, and
  // hidden inputs never take focus.
  const FOCUSABLE = [
    'a[href]',
    'area[href]',
    'button:not([disabled])',
    'input:not([disabled]):not([type="hidden"])',
    'select:not([disabled])',
    'textarea:not([disabled])',
    'iframe',
    '[contenteditable]',
    '[tabindex]:not([tabindex^="-"])',
  ].join(',');

  // Open modals, innermost last. A stack rather than a single "current" so the
  // topmost dialog is the one Escape and Tab belong to, whatever opened first.
  const stack = [];
  let listening = false;

  // top also drops anything that is no longer open. A page that hides an
  // overlay by some other route (a re-render, a reload path) would otherwise
  // leave a ghost on the stack and Tab would be trapped inside an invisible
  // dialog for the rest of the session.
  function top() {
    for (let i = stack.length - 1; i >= 0; i--) {
      if (stack[i].isOpen()) return stack[i];
      stack.splice(i, 1);
    }
    return null;
  }

  // getClientRects() is the visibility test that matters here: the modals carry
  // field groups that are display:none until a kind/provider is picked, and a
  // hidden group's inputs must not be Tab stops.
  function focusables(dialog) {
    return [...dialog.querySelectorAll(FOCUSABLE)].filter(el => el.getClientRects().length > 0);
  }

  function focusInto(m) {
    const wanted = m.opts.initialFocus ? m.opts.initialFocus() : null;
    const el = wanted || focusables(m.dialog)[0] || m.dialog;
    // A dialog with nothing focusable in it still has to hold focus itself, or
    // the next Tab resumes from the page behind the backdrop.
    if (el === m.dialog && !m.dialog.hasAttribute('tabindex')) m.dialog.tabIndex = -1;
    el.focus();
  }

  function onKeydown(e) {
    const m = top();
    if (!m) return;
    if (e.key === 'Escape') {
      m.close();
      return;
    }
    if (e.key !== 'Tab') return;
    const items = focusables(m.dialog);
    if (!items.length) {
      e.preventDefault();
      focusInto(m);
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (!m.dialog.contains(active)) {
      // Focus has already escaped (a disabled control, a removed node); take it
      // back rather than letting this Tab walk further away.
      e.preventDefault();
      (e.shiftKey ? last : first).focus();
    } else if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  // The Tab handler covers the keyboard, but focus can also arrive from outside
  // the page's control — returning from the browser's own chrome, or a control
  // inside the dialog being disabled mid-operation. This pulls it back.
  function onFocusin(e) {
    const m = top();
    if (!m || m.dialog.contains(e.target)) return;
    focusInto(m);
  }

  // Registered once and left in place: both handlers are no-ops while the stack
  // is empty, so there is nothing to unwind and no chance of a page ending up
  // with a modal open and no listener.
  function listen() {
    if (listening) return;
    document.addEventListener('keydown', onKeydown);
    document.addEventListener('focusin', onFocusin);
    listening = true;
  }

  /* attach wires one overlay and returns its handle.
     overlay — the backdrop element, or its id.
     opts:
       dialog       : selector for the dialog box inside the overlay. Defaults
                      to an existing [role="dialog"], else the first child.
       labelledBy   : id of the element naming the dialog (→ aria-labelledby).
       initialFocus : () => Element|null — what to focus on open. Falsy falls
                      back to the first focusable control.
       canClose     : () => boolean — false refuses close(), including Escape.
                      For a dialog whose operation cannot actually be cancelled.
       onClose      : () => void — runs on every close path, so state a page
                      resets when its dialog goes away (a pending plan, an edit
                      target) is not skipped by Escape. */
  function attach(overlay, opts = {}) {
    const el = typeof overlay === 'string' ? document.getElementById(overlay) : overlay;
    if (!el) throw new Error(`DevhubModal.attach: no overlay "${overlay}"`);
    const dialog = opts.dialog
      ? el.querySelector(opts.dialog)
      : el.querySelector('[role="dialog"]') || el.firstElementChild;
    if (!dialog) throw new Error(`DevhubModal.attach: no dialog inside "${el.id || overlay}"`);

    dialog.setAttribute('role', 'dialog');
    dialog.setAttribute('aria-modal', 'true');
    if (opts.labelledBy) dialog.setAttribute('aria-labelledby', opts.labelledBy);

    let returnFocus = null;

    const m = {
      overlay: el,
      dialog,
      opts,
      isOpen() { return el.classList.contains('open'); },
      open() {
        if (m.isOpen()) { focusInto(m); return; }
        // Read before the class flips, so it is whatever the user was on when
        // they asked for the dialog — on a long table, the row they clicked.
        returnFocus = document.activeElement;
        el.classList.add('open');
        stack.push(m);
        listen();
        focusInto(m);
      },
      // close reports whether it actually closed, so a caller that needs to
      // know (rather than just ask) does not have to re-read the class.
      close() {
        if (!m.isOpen()) return false;
        if (opts.canClose && !opts.canClose()) return false;
        el.classList.remove('open');
        const i = stack.indexOf(m);
        if (i >= 0) stack.splice(i, 1);
        const back = returnFocus;
        returnFocus = null;
        back?.focus?.();
        opts.onClose?.();
        return true;
      },
    };
    return m;
  }

  window.DevhubModal = { attach };
})();
