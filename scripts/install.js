#!/usr/bin/env node

const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');

// CI 环境中跳过编译，由 prepublishOnly 负责构建
if (process.env.CI === 'true') {
  console.log('CI environment detected, skipping binary compilation');
  process.exit(0);
}

console.log('Installing knowly...\n');

// 检查二进制文件是否已存在
const binDir = path.join(__dirname, '../bin');
const platform = process.platform;
const arch = process.arch;
const binaryName = platform === 'darwin' && arch === 'arm64'
  ? 'knowly-darwin-arm64'
  : platform === 'darwin'
  ? 'knowly-darwin'
  : platform === 'linux'
  ? 'knowly-linux'
  : null;

const binaryPath = binaryName ? path.join(binDir, binaryName) : null;
const binaryExists = binaryPath && fs.existsSync(binaryPath);

if (binaryExists) {
  console.log('✓ Pre-built binary found');
} else {
  // 检查 Go 环境
  try {
    execSync('go version', { stdio: 'pipe' });
    console.log('✓ Go environment found');
  } catch (e) {
    console.error('✗ Go is not installed and no pre-built binary found');
    console.error('Please install Go from https://golang.org/dl/');
    process.exit(1);
  }

  // 运行构建
  console.log('\nBuilding binaries...');
  try {
    execSync('node scripts/build.js', { stdio: 'inherit' });
  } catch (e) {
    console.error('✗ Build failed');
    process.exit(1);
  }
}

console.log('\n✓ Installation complete!');
console.log('\nRun "knowly init" to configure.');
