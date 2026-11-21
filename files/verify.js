#!/usr/bin/env node
/**
 * 输出启动器最近确认的内部语言状态。不会连接调试端口，也不检查界面文字。
 */

const fs = require('node:fs');
const path = require('node:path');
const installedLauncher = path.join(__dirname, 'codex-zh-launcher.js');
const result = { ok: false, port: null, reason: null, detail: null };

try {
  const launcher = require(fs.existsSync(installedLauncher) ? installedLauncher : './launcher.js');
  result.port = launcher.debugPort(process.argv[2] ?? 9333);
  Object.assign(result, launcher.verifyState(result.port));
} catch (err) {
  result.reason = err.message;
}

console.log(JSON.stringify(result));
process.exitCode = result.ok ? 0 : 1;
