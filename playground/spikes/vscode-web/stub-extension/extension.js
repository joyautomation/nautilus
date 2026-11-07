// Spike: what a web build of the nautilus extension does first. Start the
// wasm engine in a worker, load the workspace's project, run it, and show
// the scan count, so a harness can time "opened the page" to "it scans".
const vscode = require('vscode');

exports.activate = async function activate(ctx) {
  const t0 = Date.now();
  const item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 1000);
  item.text = 'nautilus: starting';
  item.show();
  const say = (t) => { item.text = t; console.log('[nautilus-spike] ' + t); };
  try {
    // VS Code web serves the extension over http; other hosts give it a
    // non-fetchable URI and say where its files are instead.
    const root = ctx.extension.packageJSON.spikeBase || vscode.Uri.joinPath(ctx.extensionUri, '/').toString(true);
    const base = root + 'engine/';
    const boot = `self.NAUTILUS_BASE=${JSON.stringify(base)};importScripts(${JSON.stringify(base + 'worker.js')});`;
    const worker = new Worker(URL.createObjectURL(new Blob([boot], { type: 'text/javascript' })));
    let seq = 0; const pending = new Map();
    let ready; const readyP = new Promise((r) => (ready = r));
    worker.onmessage = (e) => {
      const m = e.data;
      if (m.event === 'ready') return ready();
      if (m.event === 'fatal') return say('nautilus: engine failed: ' + m.error);
      if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.ok ? p.res(m.result) : p.rej(new Error(m.error)); }
    };
    worker.onerror = (e) => say('nautilus: worker error ' + (e.message || e));
    const call = (op, ...args) => new Promise((res, rej) => { const id = ++seq; pending.set(id, { res, rej }); worker.postMessage({ id, op, args }); });

    // The project, in memory: what the playground would load from a URL.
    const files = {};
    for (const name of ['nautilus.yaml', 'program.ld', 'sealin_test.yaml']) {
      const r = await fetch(root + 'project/' + name);
      files[name] = await r.text();
    }
    const enc = new TextEncoder();
    const emitter = new vscode.EventEmitter();
    const stat = (n) => ({ type: n === '' ? vscode.FileType.Directory : vscode.FileType.File, ctime: 0, mtime: 0, size: n === '' ? 0 : enc.encode(files[n]).length });
    ctx.subscriptions.push(vscode.workspace.registerFileSystemProvider('playground', {
      onDidChangeFile: emitter.event,
      watch: () => ({ dispose() {} }),
      stat: (uri) => { const n = uri.path.replace(/^\//, ''); if (n !== '' && !(n in files)) throw vscode.FileSystemError.FileNotFound(uri); return stat(n); },
      readDirectory: () => Object.keys(files).map((n) => [n, vscode.FileType.File]),
      readFile: (uri) => enc.encode(files[uri.path.replace(/^\//, '')]),
      writeFile: (uri, content) => { files[uri.path.replace(/^\//, '')] = new TextDecoder().decode(content); },
      createDirectory() {}, delete() {}, rename() {},
    }, { isCaseSensitive: true }));
    vscode.window.showTextDocument(vscode.Uri.parse('playground:/program.ld'));
    await readyP;
    const tEngine = Date.now() - t0;
    await call('resetProject', '/project', files);
    const r = await call('load', '/project');
    if (!r.ok) return say('nautilus: ' + r.error);
    const tRun = Date.now() - t0;
    say(`nautilus: running (engine ${tEngine} ms, run ${tRun} ms after activate)`);
    setInterval(async () => {
      const s = await call('handle', 'GET', '/api/state', {}, null);
      const st = JSON.parse(s.body);
      item.text = `nautilus: running ${st.scans} scans`;
    }, 500);
  } catch (e) {
    say('nautilus: spike error ' + (e && e.message || e));
  }
};
exports.deactivate = () => {};
