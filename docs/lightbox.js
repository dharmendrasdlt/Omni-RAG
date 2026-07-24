/* Lightweight image lightbox with pinch / scroll zoom, pan, and Esc/close.
   Applies to every content image that is NOT inside a link (so navigational
   thumbnails keep navigating). No dependencies. */
(function () {
  function ready(fn) {
    if (document.readyState !== 'loading') fn();
    else document.addEventListener('DOMContentLoaded', fn);
  }

  var overlay, imgEl, closeBtn;
  var scale = 1, tx = 0, ty = 0;
  var MIN = 1, MAX = 6;
  var pointers = new Map();
  var startDist = 0, startScale = 1, startMid = { x: 0, y: 0 }, startTx = 0, startTy = 0;
  var panStart = null;

  function dist(a, b) { return Math.hypot(a.x - b.x, a.y - b.y); }
  function mid(a, b) { return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }; }
  function clamp(s) { return Math.max(MIN, Math.min(MAX, s)); }
  function apply() { imgEl.style.transform = 'translate(' + tx + 'px,' + ty + 'px) scale(' + scale + ')'; }
  function resetView() { scale = 1; tx = 0; ty = 0; apply(); }

  function open(src, alt) {
    imgEl.src = src; imgEl.alt = alt || '';
    resetView();
    overlay.classList.add('is-open');
    overlay.setAttribute('aria-hidden', 'false');
    document.body.style.overflow = 'hidden';
  }
  function close() {
    overlay.classList.remove('is-open');
    overlay.setAttribute('aria-hidden', 'true');
    document.body.style.overflow = '';
    pointers.clear(); panStart = null;
    imgEl.removeAttribute('src');
  }

  function zoomAt(clientX, clientY, factor) {
    var rect = imgEl.getBoundingClientRect();
    var cx = rect.left + rect.width / 2;
    var cy = rect.top + rect.height / 2;
    var dx = clientX - cx, dy = clientY - cy;
    var next = clamp(scale * factor);
    var ratio = next / scale;
    tx -= dx * (ratio - 1);
    ty -= dy * (ratio - 1);
    scale = next;
    if (scale === 1) { tx = 0; ty = 0; }
    apply();
  }

  function init() {
    overlay = document.createElement('div');
    overlay.className = 'lightbox';
    overlay.setAttribute('aria-hidden', 'true');
    overlay.innerHTML =
      '<button class="lightbox__close" type="button" aria-label="Close (Esc)">&times;</button>' +
      '<img class="lightbox__img" alt="">' +
      '<div class="lightbox__hint">Pinch or scroll to zoom · double-click to reset · Esc to close</div>';
    document.body.appendChild(overlay);
    imgEl = overlay.querySelector('.lightbox__img');
    closeBtn = overlay.querySelector('.lightbox__close');

    closeBtn.addEventListener('click', close);
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && overlay.classList.contains('is-open')) close();
    });

    overlay.addEventListener('wheel', function (e) {
      if (!overlay.classList.contains('is-open')) return;
      e.preventDefault();
      zoomAt(e.clientX, e.clientY, e.deltaY < 0 ? 1.12 : 1 / 1.12);
    }, { passive: false });

    imgEl.addEventListener('pointerdown', function (e) {
      imgEl.setPointerCapture(e.pointerId);
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (pointers.size === 2) {
        var p = Array.from(pointers.values());
        startDist = dist(p[0], p[1]); startScale = scale;
        startMid = mid(p[0], p[1]); startTx = tx; startTy = ty;
      } else if (pointers.size === 1 && scale > 1) {
        panStart = { x: e.clientX, y: e.clientY, tx: tx, ty: ty };
      }
    });
    imgEl.addEventListener('pointermove', function (e) {
      if (!pointers.has(e.pointerId)) return;
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
      var p = Array.from(pointers.values());
      if (pointers.size === 2) {
        scale = clamp(startScale * (dist(p[0], p[1]) / startDist));
        var m = mid(p[0], p[1]);
        tx = startTx + (m.x - startMid.x);
        ty = startTy + (m.y - startMid.y);
        if (scale === 1) { tx = 0; ty = 0; }
        apply();
      } else if (pointers.size === 1 && panStart && scale > 1) {
        tx = panStart.tx + (e.clientX - panStart.x);
        ty = panStart.ty + (e.clientY - panStart.y);
        apply();
      }
    });
    function up(e) {
      pointers.delete(e.pointerId);
      if (pointers.size < 2) startDist = 0;
      if (pointers.size === 0) panStart = null;
      if (scale <= 1.01) resetView();
    }
    imgEl.addEventListener('pointerup', up);
    imgEl.addEventListener('pointercancel', up);
    imgEl.addEventListener('dblclick', function (e) {
      e.preventDefault();
      if (scale > 1) resetView(); else zoomAt(e.clientX, e.clientY, 2.5);
    });

    Array.prototype.forEach.call(document.querySelectorAll('img'), function (im) {
      if (im.closest('a')) return;          // keep navigational thumbnails as links
      if (im.closest('.lightbox')) return;  // skip the overlay image itself
      im.classList.add('zoomable');
      im.addEventListener('click', function () { open(im.currentSrc || im.src, im.alt); });
    });
  }

  ready(init);
})();
