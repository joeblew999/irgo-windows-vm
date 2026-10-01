//go:build darwin || windows

package drive

import (
	"encoding/json"
	"fmt"
)

// bridgeJS is installed in every page the app loads, before the page's own
// scripts. It reports what the page saw — every pointer, key, input and wheel
// event, captured at the document so the page cannot stop it, with the
// event's isTrusted flag — and the load. It only listens: it changes nothing
// in the page.
const bridgeJS = `(function () {
  if (window.__drive) return;
  function describe(el) {
    if (!el || !el.tagName) return '';
    var s = el.tagName.toLowerCase();
    if (el.id) s += '#' + el.id;
    return s;
  }
  function mods(e) {
    var m = [];
    if (e.shiftKey) m.push('shift');
    if (e.ctrlKey) m.push('control');
    if (e.altKey) m.push('alt');
    if (e.metaKey) m.push('meta');
    return m;
  }
  function send(o) {
    try { window.__drive_event(o); } catch (_) {}
  }
  var opt = { capture: true, passive: true };
  ['mousedown', 'mouseup', 'click', 'contextmenu'].forEach(function (t) {
    document.addEventListener(t, function (e) {
      send({ type: t, target: describe(e.target), x: e.clientX, y: e.clientY, button: e.button, trusted: e.isTrusted });
    }, opt);
  });
  document.addEventListener('keydown', function (e) {
    send({ type: 'keydown', target: describe(e.target), key: e.key, code: e.code, mods: mods(e), trusted: e.isTrusted });
  }, opt);
  document.addEventListener('input', function (e) {
    send({ type: 'input', target: describe(e.target), value: String(e.target.value), trusted: e.isTrusted });
  }, opt);
  window.addEventListener('wheel', function (e) {
    send({ type: 'wheel', target: describe(e.target), dx: e.deltaX, dy: e.deltaY, trusted: e.isTrusted });
  }, opt);
  window.addEventListener('load', function () { send({ type: 'ready' }); });
  window.__drive = true;
})();`

// evalJS wraps src, a script or expression, so the page answers request id
// with its JSON value (a promise is awaited) or with the error it threw.
func evalJS(id int, src string) string {
	quoted, _ := json.Marshal(src) // a string always marshals
	return fmt.Sprintf(`(async function () {
  try {
    var v = await (0, eval)(%s);
    window.__drive_reply(%d, JSON.stringify(v === undefined ? null : v), '');
  } catch (e) {
    window.__drive_reply(%d, '', String((e && e.message) || e) || 'error');
  }
})();`, quoted, id, id)
}

// rectJS answers where the element sel matches is, in CSS pixels from the
// top-left of the page's viewport, and what is drawn at its centre — so a
// click can refuse an element that is hidden, off screen or covered, as a
// user's click would miss it.
func rectJS(sel string) string {
	quoted, _ := json.Marshal(sel)
	return fmt.Sprintf(`(function () {
  var el = document.querySelector(%s);
  if (!el) return null;
  var r = el.getBoundingClientRect();
  var cx = r.left + r.width / 2, cy = r.top + r.height / 2;
  var hit = document.elementFromPoint(cx, cy);
  var top = '';
  if (hit && hit !== el && !el.contains(hit)) {
    top = hit.tagName.toLowerCase() + (hit.id ? '#' + hit.id : '');
  }
  return { x: r.left, y: r.top, w: r.width, h: r.height, vw: innerWidth, vh: innerHeight, covered: top };
})()`, quoted)
}
