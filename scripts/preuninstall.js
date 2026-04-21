#!/usr/bin/env node

// preuninstall 脚本 - 卸载前清理
const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const pidFile = path.join(require('os').homedir(), '.knowly', 'knowly.pid');

// 尝试停止运行中的守护进程
if (fs.existsSync(pidFile)) {
  try {
    const pid = fs.readFileSync(pidFile, 'utf8').trim();
    execSync(`kill ${pid} 2>/dev/null`);
    console.log('Stopped knowly daemon');
  } catch (e) {
    // 忽略错误
  }
}
