#!/usr/bin/env node
/**
 * Codex 中文界面启动器（macOS Apple Silicon，实验性）
 *
 * 原理：官方 Codex 的界面语言由远程配置层 72216192 的 enable_i18n 开关控制，
 * 该开关默认 false，只在联网且服务器下发时才会开启。本脚本以远程调试端口启动
 * 官方 Codex，并在页面脚本执行前注入补丁，强制该层返回
 * enable_i18n=true、locale_source='IDE'，从而启用应用内已打包的简体中文翻译。
 *
 * 不修改应用本体或安装语言包。仅处理自己启动的官方客户端的本机调试会话。
 *
 * 参数：
 *   --app <path>    指定 ChatGPT.app / Codex.app
 *   --port <n>      调试端口，默认 9333
 *   --verbose       输出详细日志
 */

const { execFileSync, spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { homedir } = require('node:os');
const { setTimeout: sleep } = require('node:timers/promises');

const LAYER_ID = '72216192';
const BUNDLE_ID = 'com.openai.codex';
const SIGNATURE = `anchor apple generic and identifier "${BUNDLE_ID}" and certificate leaf[subject.OU] = "2DC432GLL2"`;
const STATE_DIR = path.join(homedir(), 'Library', 'Application Support', 'CodexZhLauncher');
const STATE_MAX_AGE_MS = 10000;

function debugPort(value) {
  const port = Number(value);
  if (!Number.isInteger(port) || port < 1024 || port > 65535) {
    throw new Error('端口必须为 1024 到 65535 的整数');
  }
  return port;
}

function parseArgs(argv) {
  const out = { port: 9333, app: null, verbose: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--app') {
      out.app = argv[++i];
      if (!out.app) throw new Error('--app 需要官方应用路径');
    } else if (a === '--port') out.port = debugPort(argv[++i]);
    else if (a === '--locale') ++i; // 兼容旧的保留参数
    else if (a === '--no-quit') continue; // 现在始终由用户自行退出客户端
    else if (a === '--verbose') out.verbose = true;
    else throw new Error(`未知参数：${a}`);
  }
  return out;
}

function plistValue(file, key) {
  try {
    return execFileSync('/usr/libexec/PlistBuddy', ['-c', `Print :${key}`, file], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
  } catch {
    return '';
  }
}

function findApp(requestedPath) {
  const candidates = requestedPath ? [requestedPath] : [
    '/Applications/ChatGPT.app',
    path.join(homedir(), 'Applications/ChatGPT.app'),
    '/Applications/Codex.app',
    path.join(homedir(), 'Applications/Codex.app'),
  ];
  for (const candidate of candidates) {
    if (!fs.existsSync(candidate)) continue;
    const app = fs.realpathSync(candidate);
    const info = path.join(app, 'Contents', 'Info.plist');
    if (plistValue(info, 'CFBundleIdentifier') !== BUNDLE_ID) continue;
    const name = plistValue(info, 'CFBundleExecutable');
    if (!name || name !== path.basename(name)) continue;
    const executable = path.join(app, 'Contents', 'MacOS', name);
    if (!fs.existsSync(executable)) continue;
    const signature = spawnSync('/usr/bin/codesign', ['--verify', '--strict', `-R=${SIGNATURE}`, app], {
      stdio: 'ignore', timeout: 10000,
    });
    if (signature.status === 0) return executable;
  }
  throw new Error('未找到通过签名校验的官方 Codex 桌面应用');
}

function isRunning(executable) {
  const processes = execFileSync('/bin/ps', ['-axww', '-o', 'comm='], { encoding: 'utf8', timeout: 2000 });
  return processes.split('\n').some((command) => command.trim() === executable);
}

function processIdentity(pid) {
  if (!Number.isSafeInteger(pid) || pid < 1) return null;
  const result = spawnSync('/bin/ps', ['-ww', '-p', String(pid), '-o', 'lstart=', '-o', 'comm='], {
    encoding: 'utf8', timeout: 2000,
  });
  return result.status === 0 ? result.stdout.trim() || null : null;
}

async function launchDebug(executable, port) {
  // 直接启动已校验的可执行文件以获得确定的子进程 PID。
  const child = spawn(executable, [
    `--remote-debugging-port=${port}`, '--remote-debugging-address=127.0.0.1',
  ], { detached: true, stdio: 'ignore' });
  await new Promise((resolve, reject) => {
    child.once('spawn', resolve);
    child.once('error', reject);
  });
  child.unref();
  return child;
}

// 注入补丁：在应用脚本之前执行，拦截 Statsig 的 layer 查询
function buildInjection(layerId) {
  return `(() => {
  if (window !== window.top || location.protocol !== 'app:' || location.hostname !== '-') return;
  const LAYER = ${JSON.stringify(layerId)};
  const PATCH = { enable_i18n: true, locale_source: 'IDE' };
  if (window.__codexZhLauncher) return;
  window.__codexZhLauncher = { version: '1.0.0', at: Date.now() };

  const memo = new WeakMap();

  const patchClient = (client) => {
    if (!client || typeof client.getLayer !== 'function') return;
    if (client.getLayer.__codexZhPatched) return;
    const original = client.getLayer.bind(client);
    const wrapped = function (name, ...rest) {
      const result = original(name, ...rest);
      if (String(name) !== LAYER) return result;
      if (!result || typeof result !== 'object') return result;
      const hit = memo.get(result);
      if (hit) return hit;
      const base = result.value && typeof result.value === 'object' ? result.value : {};
      const value = { ...base, ...PATCH };
      const patched = {
        ...result,
        value,
        get(key, fallback) {
          if (Object.prototype.hasOwnProperty.call(PATCH, key)) return PATCH[key];
          if (typeof result.get === 'function') return result.get(key, fallback);
          return value[key] !== undefined ? value[key] : fallback;
        },
      };
      memo.set(result, patched);
      return patched;
    };
    wrapped.__codexZhPatched = true;
    try { client.getLayer = wrapped; } catch {}
  };

  const patchRoot = (root) => {
    if (!root || typeof root !== 'object') return;
    const clients = new Set();
    if (root.firstInstance) clients.add(root.firstInstance);
    if (root.instances && typeof root.instances === 'object') {
      for (const v of Object.values(root.instances)) clients.add(v);
    }
    if (typeof root.instance === 'function') { try { clients.add(root.instance()); } catch {} }
    for (const c of clients) patchClient(c);
  };

  let current = window.__STATSIG__;
  const desc = Object.getOwnPropertyDescriptor(window, '__STATSIG__');
  try {
    Object.defineProperty(window, '__STATSIG__', {
      configurable: true,
      enumerable: desc ? desc.enumerable : true,
      get() { return desc && desc.get ? desc.get.call(this) : current; },
      set(v) {
        if (desc && desc.set) desc.set.call(this, v);
        current = v;
        patchRoot(v);
      },
    });
  } catch {}
  patchRoot(current);

  const timer = setInterval(() => {
    patchRoot(window.__STATSIG__);
    if (window.__STATSIG__ && window.__STATSIG__.firstInstance) clearInterval(timer);
  }, 500);
  setTimeout(() => clearInterval(timer), 120000);
})();`;
}

function listeners(port) {
  const result = spawnSync('/usr/sbin/lsof', ['-a', '-nP', '-iTCP:' + port, '-sTCP:LISTEN', '-Fpn'], {
    encoding: 'utf8', timeout: 2000, stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (result.status === 1 && !result.stdout && !result.stderr) return [];
  if (result.error || result.status !== 0 || result.stderr) throw new Error('无法检查调试端口的监听进程');

  const endpoints = [];
  let pid;
  for (const line of result.stdout.split('\n')) {
    if (line.startsWith('p')) pid = Number(line.slice(1));
    if (line.startsWith('n')) endpoints.push({ pid, address: line.slice(1) });
  }
  if (!endpoints.length) throw new Error('无法识别调试端口的监听信息');
  return endpoints;
}

function hasOwnListener({ appPid, port }) {
  const endpoints = listeners(port);
  if (!endpoints.length) return false;
  if (!endpoints.every(({ pid, address }) => pid === appPid &&
    (address === `127.0.0.1:${port}` || address === `[::1]:${port}`))) {
    throw new Error('调试端口的归属或监听地址与本次启动不符');
  }
  return endpoints.some(({ address }) => address === `127.0.0.1:${port}`);
}

function httpGetJson(port, pathname) {
  return new Promise((resolve, reject) => {
    const req = http.get({ host: '127.0.0.1', port, path: pathname, timeout: 1500, agent: false }, (res) => {
      res.on('error', reject);
      if (res.statusCode !== 200) {
        res.resume();
        reject(new Error('调试端口没有返回有效页面列表'));
        return;
      }
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (c) => {
        body += c;
        if (body.length > 1024 * 1024) req.destroy(new Error('调试页面列表过大'));
      });
      res.on('end', () => {
        try { resolve(JSON.parse(body)); } catch (e) { reject(e); }
      });
    });
    req.on('timeout', () => { req.destroy(new Error('timeout')); });
    req.on('error', reject);
  });
}

function cdpConnect(wsUrl) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(wsUrl);
    const pending = new Map();
    let seq = 0;
    const conn = {
      send(method, params) {
        const id = ++seq;
        return new Promise((res, rej) => {
          const timer = setTimeout(() => {
            pending.delete(id);
            rej(new Error('调试命令超时'));
          }, 3000);
          pending.set(id, { res, rej, timer });
          try {
            ws.send(JSON.stringify({ id, method, params }));
          } catch (err) {
            clearTimeout(timer);
            pending.delete(id);
            rej(err);
          }
        });
      },
      close() { try { ws.close(); } catch {} },
      ws,
    };
    const connecting = setTimeout(() => {
      reject(new Error('调试连接超时'));
      conn.close();
    }, 3000);
    ws.addEventListener('open', () => {
      clearTimeout(connecting);
      resolve(conn);
    });
    ws.addEventListener('error', () => {
      clearTimeout(connecting);
      reject(new Error('调试连接失败'));
      conn.close();
    });
    ws.addEventListener('close', () => {
      clearTimeout(connecting);
      reject(new Error('调试连接已关闭'));
      for (const { rej, timer } of pending.values()) {
        clearTimeout(timer);
        rej(new Error('调试连接已关闭'));
      }
      pending.clear();
    });
    ws.addEventListener('message', (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch { return; }
      if (msg.id && pending.has(msg.id)) {
        const { res, rej, timer } = pending.get(msg.id);
        clearTimeout(timer);
        pending.delete(msg.id);
        if (msg.error) rej(new Error('调试命令未成功执行')); else res(msg.result);
      }
    });
  });
}

function desktopPages(targets, port) {
  if (!Array.isArray(targets)) return [];
  return targets.filter((target) => {
    if (target?.type !== 'page' || typeof target.id !== 'string' || !/^[\da-f-]+$/i.test(target.id)) return false;
    try {
      const page = new URL(target.url);
      const socket = new URL(target.webSocketDebuggerUrl);
      return page.protocol === 'app:' && page.hostname === '-' &&
        socket.href === `ws://127.0.0.1:${port}/devtools/page/${target.id}`;
    } catch {
      return false;
    }
  });
}

function statePath(port) {
  return path.join(STATE_DIR, `session-${port}.json`);
}

function ensureStateDir() {
  fs.mkdirSync(STATE_DIR, { recursive: true, mode: 0o700 });
  const stat = fs.lstatSync(STATE_DIR);
  if (!stat.isDirectory() || stat.uid !== process.getuid() || (stat.mode & 0o022)) {
    throw new Error('无法安全写入启动器会话目录');
  }
}

function writeState(session, result) {
  const file = statePath(session.port);
  const temp = `${file}.${process.pid}.tmp`;
  try {
    fs.writeFileSync(temp, JSON.stringify({ ...session, checkedAt: Date.now(), result }), { mode: 0o600, flag: 'wx' });
    fs.renameSync(temp, file);
  } finally {
    try { fs.unlinkSync(temp); } catch {}
  }
}

function readState(port) {
  const fd = fs.openSync(statePath(port), fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const stat = fs.fstatSync(fd);
    if (!stat.isFile() || stat.uid !== process.getuid() || (stat.mode & 0o077) || stat.size > 8192) {
      throw new Error('无法确认启动器会话记录');
    }
    return JSON.parse(fs.readFileSync(fd, 'utf8'));
  } finally {
    fs.closeSync(fd);
  }
}

function verifyState(port) {
  let state;
  try {
    state = readState(port);
  } catch {
    throw new Error('未找到可验证的启动器会话，请通过「Codex 中文」启动后重试');
  }
  const age = Date.now() - state?.checkedAt;
  if (state?.port !== port || typeof state.result?.ok !== 'boolean' || !Number.isFinite(age) ||
    age < 0 || age > STATE_MAX_AGE_MS ||
    !state.daemonIdentity || state.daemonIdentity !== processIdentity(state.daemonPid) ||
    !state.appIdentity || state.appIdentity !== processIdentity(state.appPid) || !hasOwnListener(state)) {
    throw new Error('启动器会话已经退出或无法确认，无法验证内部状态');
  }
  return state.result;
}

// 这里只读取语言设置和补丁状态，不能判定界面实际显示了什么文字。
const STATUS_EXPRESSION = `(() => {
  if (window !== window.top || location.protocol !== 'app:' || location.hostname !== '-') return null;
  const root = window.__STATSIG__;
  const c = root && (root.firstInstance || (root.instance && root.instance()));
  const layer = c && c.getLayer ? c.getLayer('${LAYER_ID}') : null;
  return {
    injected: !!window.__codexZhLauncher,
    wrapper: !!(c && c.getLayer && c.getLayer.__codexZhPatched),
    enable_i18n: layer && layer.get ? layer.get('enable_i18n', null) : null,
    locale_source: layer && layer.get ? layer.get('locale_source', null) : null,
    htmlLang: document.documentElement.lang
  };
})()`;

async function injectTarget(session, target, source) {
  if (!hasOwnListener(session)) throw new Error('调试端口已经关闭');
  const conn = await cdpConnect(target.webSocketDebuggerUrl);
  try {
    if (!hasOwnListener(session)) throw new Error('调试端口已经关闭');
    await conn.send('Page.addScriptToEvaluateOnNewDocument', { source });
    await conn.send('Page.reload', { ignoreCache: false });
    return conn;
  } catch (err) {
    conn.close();
    throw err;
  }
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (process.platform !== 'darwin' || process.arch !== 'arm64' || typeof WebSocket !== 'function') {
    throw new Error('当前启动器需要 macOS Apple Silicon 和支持 WebSocket 的 Node.js 运行时');
  }
  const executable = findApp(opts.app);
  if (isRunning(executable)) throw new Error('请退出正在运行的 Codex，再通过「Codex 中文」启动');
  if (listeners(opts.port).length) throw new Error('调试端口已被占用，启动器不会连接现有会话');
  ensureStateDir();
  const daemonIdentity = processIdentity(process.pid);
  if (!daemonIdentity) throw new Error('无法识别启动器进程');

  const child = await launchDebug(executable, opts.port);
  const session = { port: opts.port, appPid: child.pid, daemonPid: process.pid,
    appIdentity: processIdentity(child.pid), daemonIdentity };
  const source = buildInjection(LAYER_ID);
  const connections = new Map();
  const deadline = Date.now() + 45000;
  let seenPage = false;
  let ready = false;
  let confirmedOnce = false;

  try {
    while (child.exitCode === null && child.signalCode === null) {
      let pages = [];
      if (session.appIdentity && hasOwnListener(session)) {
        const targets = await httpGetJson(opts.port, '/json/list').catch(() => null);
        if (targets && hasOwnListener(session)) pages = desktopPages(targets, opts.port);
      }
      if (pages.length) seenPage = true;

      let detail = null;
      for (const [id, conn] of connections) {
        if (conn.ws.readyState !== 1) connections.delete(id);
      }
      for (const page of pages) {
        try {
          let conn = connections.get(page.id);
          if (!conn) {
            conn = await injectTarget(session, page, source);
            connections.set(page.id, conn);
          }
          const evaluated = await conn.send('Runtime.evaluate', { expression: STATUS_EXPRESSION, returnByValue: true });
          const status = evaluated.result?.value;
          if (!detail || (status?.injected && status?.wrapper && status?.enable_i18n === true)) detail = status || null;
        } catch {
          if (opts.verbose) console.log('当前窗口的内部状态暂不可用，稍后重试');
        }
      }
      if (seenPage && hasOwnListener(session)) {
        const ok = !!(detail?.injected && detail?.wrapper && detail?.enable_i18n === true);
        if (ok && !ready && opts.verbose) console.log('已确认语言补丁的内部状态');
        ready = ok;
        confirmedOnce ||= ok;
        writeState(session, { ok, port: opts.port, reason: ok ? null : '尚未确认语言补丁的内部状态', detail });
      }
      if (!confirmedOnce && Date.now() > deadline) {
        throw new Error(seenPage ? '未能确认语言补丁状态，当前客户端版本可能不兼容' : '没有找到兼容的 Codex 桌面页面');
      }
      await sleep(seenPage ? 1500 : 500);
    }
    if (!confirmedOnce) throw new Error('Codex 在实验性会话就绪前退出');
  } catch (err) {
    throw new Error(`${err.message}。请退出 Codex，然后从「应用程序」正常打开官方客户端。`);
  } finally {
    for (const conn of connections.values()) conn.close();
    try {
      if (readState(opts.port).daemonPid === process.pid) fs.unlinkSync(statePath(opts.port));
    } catch {
      // 没有会话记录，或已由另一会话写入。
    }
  }
}

if (require.main === module) {
  main().catch((err) => {
    console.error(`启动失败：${err.message}`);
    process.exitCode = 1;
  });
}

module.exports = { debugPort, verifyState };
