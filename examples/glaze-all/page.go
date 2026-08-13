package main

// The interactive page.
//
// Inline, with no build step and no assets: this example must stay a `go run`
// away on a machine with nothing installed, which is the same property that
// lets the probes cross-compile to Windows with no toolchain. A bundler here
// would be the one thing in the repository that needs node.
//
// Every button calls a Go function bound with WebView.Bind, which glaze exposes
// as a global returning a promise. Results land under the button that caused
// them; anything asynchronous — a tray click, a menu choice, a second copy of
// the program — arrives on the events bridge and goes to the log.
const interactiveHTML = `<!doctype html>
<meta charset="utf-8">
<title>irgo — native capabilities</title>
<style>
  :root {
    color-scheme: light dark;
    --bg: #fbfbfd; --card: #fff; --line: #e3e3e8; --ink: #16161a;
    --dim: #6a6a75; --accent: #2f6fd0; --ok: #1a7f4b; --bad: #b3261e;
    --code: #f2f2f6;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #16161a; --card: #1e1e24; --line: #2e2e37; --ink: #ececf1;
      --dim: #9a9aa6; --accent: #6aa3f0; --ok: #4ec98a; --bad: #ff8b80;
      --code: #26262e;
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: var(--bg); color: var(--ink);
    font: 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif;
  }
  header { padding: 20px 24px 8px; }
  h1 { margin: 0; font-size: 17px; letter-spacing: -0.01em; }
  header p { margin: 4px 0 0; color: var(--dim); font-size: 13px; }
  main {
    display: grid; gap: 12px; padding: 16px 24px 24px;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  }
  section {
    background: var(--card); border: 1px solid var(--line);
    border-radius: 10px; padding: 14px;
  }
  h2 {
    margin: 0 0 2px; font-size: 13px; font-weight: 600;
    display: flex; align-items: baseline; gap: 8px;
  }
  h2 code { font-size: 11px; color: var(--dim); font-weight: 400; }
  .why { margin: 0 0 10px; font-size: 12px; color: var(--dim); }
  .row { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  button {
    font: inherit; font-size: 13px; padding: 5px 11px; cursor: pointer;
    background: var(--accent); color: #fff; border: 0; border-radius: 6px;
  }
  button.plain { background: transparent; color: var(--accent); box-shadow: inset 0 0 0 1px var(--line); }
  button:active { transform: translateY(1px); }
  input {
    font: inherit; font-size: 13px; padding: 5px 8px; flex: 1; min-width: 90px;
    border: 1px solid var(--line); border-radius: 6px;
    background: var(--bg); color: var(--ink);
  }
  .out {
    margin-top: 9px; font: 12px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    white-space: pre-wrap; word-break: break-word; color: var(--dim);
    background: var(--code); border-radius: 6px; padding: 7px 9px; min-height: 30px;
  }
  .out.ok { color: var(--ok); }
  .out.bad { color: var(--bad); }
  #logwrap { padding: 0 24px 28px; }
  #log {
    background: var(--code); border: 1px solid var(--line); border-radius: 10px;
    padding: 10px 12px; height: 190px; overflow: auto;
    font: 12px/1.55 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  }
  #log b { color: var(--accent); font-weight: 600; }
  .note { color: var(--dim); font-size: 12px; margin: 8px 0 0; }
</style>

<header>
  <h1>Every native capability, by hand</h1>
  <p>Nothing here is destructive. The clipboard is the only thing changed, and only when you ask.</p>
</header>

<main>
  <section>
    <h2>Clipboard <code>native/clipboard</code></h2>
    <p class="why">Round trip through the real system clipboard — paste into any other app to check.</p>
    <div class="row">
      <input id="clip" value="hello from irgo">
      <button onclick="go(this,'clipWrite',val('clip'))">Write</button>
      <button class="plain" onclick="go(this,'clipRead')">Read</button>
    </div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Keep awake <code>native/power</code></h2>
    <p class="why">Holds a system sleep assertion. Leave it held and the machine will not doze off.</p>
    <div class="row">
      <input id="reason" value="irgo example is busy">
      <button onclick="go(this,'powerHold',val('reason'))">Hold</button>
      <button class="plain" onclick="go(this,'powerRelease')">Release</button>
    </div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Tray icon <code>native/tray</code></h2>
    <p class="why">Raised and left up. Click it in the menu bar or notification area — its items report back here.</p>
    <div class="row">
      <button onclick="go(this,'trayShow')">Raise</button>
      <button class="plain" onclick="go(this,'trayHide')">Hide</button>
    </div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Menu bar <code>glaze/menu</code></h2>
    <p class="why">A real native menu. The Edit items wire to the responder chain, so Cmd+C/V work in the boxes on this page.</p>
    <div class="row">
      <button onclick="go(this,'menuInstall')">Install</button>
      <button class="plain" onclick="go(this,'menuRemove')">Remove</button>
    </div>
    <div class="out"></div>
  </section>

  <section>
    <h2>File dialogs <code>glaze</code></h2>
    <p class="why">All four, native and modal. Cancelling is not an error — it returns an empty path.</p>
    <div class="row">
      <button onclick="go(this,'dlgOpen')">Open</button>
      <button onclick="go(this,'dlgOpenMulti')">Open many</button>
      <button onclick="go(this,'dlgSave')">Save as</button>
      <button onclick="go(this,'dlgDir')">Folder</button>
    </div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Open a URL <code>native/openurl</code></h2>
    <p class="why">Handed to the default handler. Only http, https, mailto and file are allowed — try <code>ms-settings:privacy</code> and watch it refuse.</p>
    <div class="row">
      <input id="url" value="https://github.com/crgimenes/native">
      <button onclick="go(this,'openURL',val('url'))">Open</button>
    </div>
    <div class="row" style="margin-top:6px">
      <input id="reveal" placeholder="a path (blank = your home folder)">
      <button class="plain" onclick="go(this,'revealPath',val('reveal'))">Reveal</button>
    </div>
    <div class="out"></div>
  </section>

  <section>
    <h2>App icon <code>glaze.SetAppIcon</code></h2>
    <p class="why">Changes the icon of the running process. Click a few times and watch the Dock. Windows and Linux take theirs from the executable, so both refuse — correctly.</p>
    <div class="row"><button onclick="go(this,'iconSet')">Next colour</button></div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Hide from capture <code>native/nocapture</code></h2>
    <p class="why">Windows only. Afterwards a screenshot shows this window black while you keep seeing it. There is no way to undo it, so it is a one-way trip.</p>
    <div class="row"><button onclick="go(this,'noCapture')">Protect this window</button></div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Memory mapping <code>native/mmap</code></h2>
    <p class="why">Maps a temp file, upper-cases it <em>through the mapping</em>, then reads the file back off disk to prove the write went through.</p>
    <div class="row">
      <input id="mm" value="mapped memory">
      <button onclick="go(this,'mmapDemo',val('mm'))">Map and write</button>
    </div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Single instance <code>native/singleinstance</code></h2>
    <p class="why">This copy holds the lock. Start a second one and it will hand its arguments to this window instead of opening another.</p>
    <div class="row"><button class="plain" onclick="go(this,'instanceHint')">Show me the command</button></div>
    <div class="out"></div>
  </section>

  <section>
    <h2>Events bridge <code>glaze.Events</code></h2>
    <p class="why">The other direction: the page emits, Go receives. Every button above is the Go-to-page half already.</p>
    <div class="row"><button class="plain" onclick="glaze.events.emit('ui:ping','the button')">Emit ui:ping</button></div>
    <div class="out"></div>
  </section>

  <section>
    <h2>The unattended report</h2>
    <p class="why">Exactly what <code>glaze-all</code> prints with no flags — the thing the VM runs. It will raise one file dialog.</p>
    <div class="row"><button onclick="go(this,'runReport')">Run it</button></div>
    <div class="out"></div>
  </section>
</main>

<div id="logwrap">
  <div id="log"></div>
  <p class="note">Tray clicks, menu choices and messages from a second copy arrive here.</p>
</div>

<script>
  const val = id => document.getElementById(id).value;

  function stamp() {
    return new Date().toTimeString().slice(0, 8);
  }

  function line(topic, text) {
    const log = document.getElementById('log');
    const div = document.createElement('div');
    div.innerHTML = stamp() + ' <b>' + topic + '</b> ';
    div.appendChild(document.createTextNode(text));
    log.appendChild(div);
    log.scrollTop = log.scrollHeight;
  }

  // Every button goes through here so a rejected promise is shown rather than
  // vanishing into the console — an unsupported platform is a normal answer.
  async function go(btn, fn, ...args) {
    const out = btn.closest('section').querySelector('.out');
    out.className = 'out';
    out.textContent = '…';
    try {
      out.textContent = await window[fn](...args);
      out.className = 'out ok';
    } catch (e) {
      out.textContent = String(e && e.message ? e.message : e);
      out.className = 'out bad';
    }
  }

  glaze.events.on('log', (topic, text) => line(topic, text));
</script>
`
