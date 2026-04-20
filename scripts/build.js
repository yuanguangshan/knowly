#!/usr/bin/env node

const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const os = require('os');

console.log('Building knas...\n');

// 确保 bin 目录存在
const binDir = path.join(__dirname, '..', 'bin');
if (!fs.existsSync(binDir)) {
  fs.mkdirSync(binDir, { recursive: true });
}

// 定义构建目标
const targets = [
  { goos: 'darwin', goarch: 'amd64', output: 'knas-darwin' },
  { goos: 'darwin', goarch: 'arm64', output: 'knas-darwin-arm64' },
  { goos: 'linux', goarch: 'amd64', output: 'knas-linux' },
];

// 构建每个目标
targets.forEach(target => {
  console.log(`Building for ${target.goos}-${target.goarch}...`);

  const env = {
    ...process.env,
    GOOS: target.goos,
    GOARCH: target.goarch,
    CGO_ENABLED: target.goos === 'darwin' ? '1' : '0'
  };

  if (target.goos === 'darwin') {
    try {
      env.SDKROOT = execSync('xcrun --sdk macosx --show-sdk-path', { encoding: 'utf8' }).trim();
    } catch (_) {}
  }

  const outputPath = path.join(binDir, target.output);

  try {
    execSync(`go build -o ${outputPath} -ldflags="-s -w" ./cmd/knas`, {
      env,
      stdio: 'inherit'
    });

    // 设置可执行权限
    fs.chmodSync(outputPath, '755');

    console.log(`✓ Built ${target.output}`);
  } catch (e) {
    console.error(`✗ Failed to build ${target.output}`);
    process.exit(1);
  }
});

console.log('\n✓ Build complete!');
console.log('\nBinaries:');
targets.forEach(target => {
  const size = fs.statSync(path.join(binDir, target.output)).size;
  console.log(`  ${target.output}: ${(size / 1024).toFixed(2)} KB`);
});
