/* devhub 共通ドラッグ並び替えモジュール。
 *
 * フレームワーク非依存・素の HTML5 Drag & Drop。各ツールの index.html から
 *   <script src="/shared/reorder.js"></script>
 * で読み込み、再レンダ後に DevhubReorder.attach(container, opts) を呼ぶ。
 *
 * 設計方針: このモジュールは「どの DOM をドラッグ可能にし、drop 時に
 * どの key がどの key の位置へ移ったか」だけを扱う。実データ（配列）の
 * 並び替えと永続化は呼び出し側が onDrop コールバックで行う。
 */
(function () {
  'use strict';

  // keys 配列上で srcKey を dstKey の位置へ移した新しい配列を返す純粋関数。
  // dashboard の applyReorder と同じ splice ロジック。元配列は変更しない。
  function move(keys, srcKey, dstKey) {
    var arr = keys.slice();
    if (srcKey === dstKey) return arr;
    var srcIdx = arr.indexOf(srcKey);
    var dstIdx = arr.indexOf(dstKey);
    if (srcIdx < 0 || dstIdx < 0) return arr;
    var item = arr.splice(srcIdx, 1)[0];
    arr.splice(srcIdx < dstIdx ? dstIdx - 1 : dstIdx, 0, item);
    return arr;
  }

  // container 内の itemSelector に一致する要素をドラッグ並び替え可能にする。
  //
  // opts:
  //   itemSelector   : 並び替え対象行/カードのセレクタ（必須）
  //   keyAttr        : key を保持する属性名（既定 'data-key'）
  //   handleSelector : 指定時はこのハンドル要素からのみドラッグ開始（入力欄を
  //                    含む行向け）。未指定なら item 自体を draggable にする。
  //   onDrop(src,dst): drop 時に呼ばれる。src を dst の位置へ動かす意図。
  //   cls            : ドロップ位置インジケータの class（既定 'insert-before'）
  //
  // 再レンダ後（DOM 差し替え後）に再度呼べばよい。新しいノードへ配線し直す。
  function attach(container, opts) {
    if (!container) return;
    opts = opts || {};
    var itemSelector = opts.itemSelector;
    var keyAttr = opts.keyAttr || 'data-key';
    var handleSelector = opts.handleSelector || null;
    var onDrop = opts.onDrop || function () {};
    var cls = opts.cls || 'insert-before';
    if (!itemSelector) return;

    var srcKey = null;

    function clearMarks() {
      var all = container.querySelectorAll(itemSelector);
      for (var i = 0; i < all.length; i++) all[i].classList.remove(cls);
    }

    var items = container.querySelectorAll(itemSelector);
    for (var i = 0; i < items.length; i++) {
      (function (item) {
        var dragEl = handleSelector ? item.querySelector(handleSelector) : item;
        if (!dragEl) return;
        dragEl.setAttribute('draggable', 'true');

        dragEl.addEventListener('dragstart', function (e) {
          srcKey = item.getAttribute(keyAttr);
          if (e.dataTransfer) {
            e.dataTransfer.effectAllowed = 'move';
            try { e.dataTransfer.setData('text/plain', srcKey == null ? '' : srcKey); } catch (_) {}
          }
          var el = item;
          setTimeout(function () { el.style.opacity = '0.4'; }, 0);
        });

        dragEl.addEventListener('dragend', function () {
          item.style.opacity = '';
          clearMarks();
          srcKey = null;
        });

        item.addEventListener('dragover', function (e) {
          // Only react to a drag that started from THIS container. Nested lists
          // (e.g. process rows inside an env card) share bubbling events, and
          // each attach() has its own srcKey closure, so this scopes the drop
          // target and the highlight to the matching list.
          if (srcKey == null) return;
          e.preventDefault();
          if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
          item.classList.add(cls);
        });

        item.addEventListener('dragleave', function () {
          item.classList.remove(cls);
        });

        item.addEventListener('drop', function (e) {
          if (srcKey == null) return;
          e.preventDefault();
          item.classList.remove(cls);
          var dstKey = item.getAttribute(keyAttr);
          if (dstKey != null && srcKey !== dstKey) {
            onDrop(srcKey, dstKey);
          }
        });
      })(items[i]);
    }
  }

  window.DevhubReorder = { move: move, attach: attach };
})();
