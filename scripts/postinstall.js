#!/usr/bin/env node

// postinstall 脚本 - 安装后检查
const fs = require('fs');
const path = require('path');

const binDir = path.join(__dirname, '..', 'bin');
const requiredBinaries = ['knowly-darwin', 'knowly-darwin-arm64', 'knowly-linux'];

let allExist = true;
requiredBinaries.forEach(bin => {
  const binPath = path.join(binDir, bin);
  if (!fs.existsSync(binPath)) {
    allExist = false;
  }
});

if (!allExist) {
  console.warn('Warning: Some binaries are missing. Run "npm run build" to build them.');
}
