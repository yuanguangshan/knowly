#!/usr/bin/env node

const { program } = require('commander');
const inquirer = require('inquirer');
const chalk = require('chalk');
const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');
const os = require('os');

program
  .name('knas')
  .description('Knowledge Async - Clipboard to NAS sync tool')
  .version(require('../package.json').version);

const CONFIG_DIR = path.join(os.homedir(), '.knas');
const CONFIG_FILE = path.join(CONFIG_DIR, 'config.json');
const LOG_FILE = path.join(CONFIG_DIR, 'knas.log');
const PID_FILE = path.join(CONFIG_DIR, 'knas.pid');

// 获取平台对应的二进制文件名
function getBinaryPath() {
  const platform = os.platform();
  const arch = os.arch();
  const binDir = path.join(__dirname, '..', 'bin');

  let binaryName;
  if (platform === 'darwin') {
    binaryName = 'knas-darwin';
  } else if (platform === 'linux') {
    binaryName = 'knas-linux';
  } else {
    throw new Error(`Unsupported platform: ${platform}`);
  }

  return path.join(binDir, binaryName);
}

// 检查配置是否存在
function isConfigured() {
  return fs.existsSync(CONFIG_FILE);
}

// 读取配置
function loadConfig() {
  if (!isConfigured()) {
    return null;
  }
  return JSON.parse(fs.readFileSync(CONFIG_FILE, 'utf8'));
}

// 保存配置
function saveConfig(config) {
  if (!fs.existsSync(CONFIG_DIR)) {
    fs.mkdirSync(CONFIG_DIR, { recursive: true });
  }
  fs.writeFileSync(CONFIG_FILE, JSON.stringify(config, null, 2));
}

// 检查是否在运行
function isRunning() {
  if (!fs.existsSync(PID_FILE)) {
    return false;
  }

  try {
    const pid = parseInt(fs.readFileSync(PID_FILE, 'utf8'));
    process.kill(pid, 0); // 检查进程是否存在
    return true;
  } catch (e) {
    return false;
  }
}

// 执行二进制文件
function execBinary(args = [], detached = false) {
  const binary = getBinaryPath();

  if (!fs.existsSync(binary)) {
    console.error(chalk.red(`Error: Binary not found at ${binary}`));
    console.error(chalk.yellow('Please run: npm run build'));
    process.exit(1);
  }

  const options = { stdio: 'inherit' };
  if (detached) {
    options.detached = true;
    options.windowsHide = true;
  }

  const child = spawn(binary, args, options);

  if (detached) {
    child.unref();
  }

  return child;
}

// 初始化命令
program
  .command('init')
  .description('Initialize knas configuration')
  .action(async () => {
    console.log(chalk.cyan('Welcome to knas (Knowledge Async)!\n'));

    const defaultConfig = {
      ssh: {
        host: '',
        port: '22',
        user: 'root',
        key_path: path.join(os.homedir(), '.ssh', 'id_rsa'),
        base_path: '~/knas_archive'
      },
      clipboard: {
        min_length: 100,
        poll_interval_ms: 500,
        exclude_words: ['password', '密码', 'token']
      },
      sync: {
        enabled: true,
        max_retries: 3,
        retry_delay_ms: 5000
      }
    };

    const answers = await inquirer.prompt([
      {
        type: 'input',
        name: 'host',
        message: 'SSH host address:',
        validate: input => input.length > 0 || 'Host is required'
      },
      {
        type: 'input',
        name: 'port',
        message: 'SSH port:',
        default: '22'
      },
      {
        type: 'input',
        name: 'user',
        message: 'SSH user:',
        default: 'root'
      },
      {
        type: 'input',
        name: 'key_path',
        message: 'SSH private key path:',
        default: path.join(os.homedir(), '.ssh', 'id_rsa'),
        validate: input => fs.existsSync(input) || 'Key file does not exist'
      },
      {
        type: 'input',
        name: 'base_path',
        message: 'Remote base path:',
        default: '~/knas_archive'
      },
      {
        type: 'number',
        name: 'min_length',
        message: 'Minimum clipboard length to sync:',
        default: 100
      }
    ]);

    defaultConfig.ssh.host = answers.host;
    defaultConfig.ssh.port = answers.port;
    defaultConfig.ssh.user = answers.user;
    defaultConfig.ssh.key_path = answers.key_path;
    defaultConfig.ssh.base_path = answers.base_path;
    defaultConfig.clipboard.min_length = answers.min_length;

    saveConfig(defaultConfig);

    console.log(chalk.green('\n✓ Configuration saved to'), CONFIG_FILE);
    console.log(chalk.cyan('\nYou can now start the daemon with: knas start'));
  });

// 启动命令
program
  .command('start')
  .description('Start knas daemon')
  .action(() => {
    if (!isConfigured()) {
      console.error(chalk.red('Error: knas is not configured'));
      console.error(chalk.yellow('Run "knas init" to configure'));
      process.exit(1);
    }

    if (isRunning()) {
      console.log(chalk.yellow('knas daemon is already running'));
      return;
    }

    console.log(chalk.cyan('Starting knas daemon...'));
    execBinary(['--daemon'], true);
    console.log(chalk.green('✓ knas daemon started'));
    console.log(chalk.gray(`Log file: ${LOG_FILE}`));
  });

// 停止命令
program
  .command('stop')
  .description('Stop knas daemon')
  .action(() => {
    if (!isRunning()) {
      console.log(chalk.yellow('knas daemon is not running'));
      return;
    }

    console.log(chalk.cyan('Stopping knas daemon...'));
    execBinary(['--stop']);
    console.log(chalk.green('✓ knas daemon stopped'));
  });

// 状态命令
program
  .command('status')
  .description('Show knas daemon status')
  .action(() => {
    execBinary(['--status']);
  });

// 日志命令
program
  .command('log')
  .description('Show knas logs')
  .option('-f, --follow', 'Follow log output')
  .action((options) => {
    if (!fs.existsSync(LOG_FILE)) {
      console.log(chalk.yellow('No log file found'));
      return;
    }

    if (options.follow) {
      const tail = spawn('tail', ['-f', LOG_FILE], { stdio: 'inherit' });
      tail.on('error', () => {
        console.log(chalk.yellow('Log file not found or cannot be read'));
      });
    } else {
      const logContent = fs.readFileSync(LOG_FILE, 'utf8');
      console.log(logContent);
    }
  });

// 服务安装命令
program
  .command('service install')
  .description('Install knas as macOS LaunchAgent')
  .action(() => {
    if (!isConfigured()) {
      console.error(chalk.red('Error: knas is not configured'));
      console.error(chalk.yellow('Run "knas init" to configure'));
      process.exit(1);
    }

    const plistContent = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.knas.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>${getBinaryPath()}</string>
        <string>--daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>${LOG_FILE}</string>
    <key>StandardErrorPath</key>
    <string>${LOG_FILE}</string>
    <key>WorkingDirectory</key>
    <string>${CONFIG_DIR}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>GODEBUG</key>
        <string>netdns=cgo</string>
    </dict>
    <key>SessionCreate</key>
    <true/>
</dict>
</plist>`;

    const plistPath = path.join(os.homedir(), 'Library', 'LaunchAgents', 'com.knas.daemon.plist');

    try {
      fs.writeFileSync(plistPath, plistContent);
      console.log(chalk.green('✓ LaunchAgent installed at'), plistPath);
      console.log(chalk.cyan('\nTo load the service, run:'));
      console.log(chalk.gray('  launchctl load ~/Library/LaunchAgents/com.knas.daemon.plist'));
      console.log(chalk.cyan('\nTo unload the service, run:'));
      console.log(chalk.gray('  launchctl unload ~/Library/LaunchAgents/com.knas.daemon.plist'));
    } catch (e) {
      console.error(chalk.red('Error installing LaunchAgent:'), e.message);
      process.exit(1);
    }
  });

// 配置命令
program
  .command('config')
  .description('Show or edit configuration')
  .option('-e, --edit', 'Edit configuration')
  .action((options) => {
    if (!isConfigured()) {
      console.error(chalk.red('Error: knas is not configured'));
      console.error(chalk.yellow('Run "knas init" to configure'));
      process.exit(1);
    }

    const cfg = loadConfig();

    if (options.edit) {
      const editor = process.env.EDITOR || 'vim';
      spawn(editor, [CONFIG_FILE], { stdio: 'inherit' });
    } else {
      console.log(JSON.stringify(cfg, null, 2));
    }
  });

// 历史命令
program
  .command('history [n]')
  .description('Show recent history entries (default: 20)')
  .action((n) => {
    const args = ['history'];
    if (n) args.push(String(n));
    execBinary(args);
  });

// 恢复命令
program
  .command('restore <id>')
  .description('Restore a history entry to clipboard')
  .action((id) => {
    execBinary(['restore', id]);
  });

// 版本命令
program
  .command('version')
  .description('Show version information')
  .action(() => {
    const pkg = require('../package.json');
    console.log(`knas v${pkg.version}`);
  });

// 解析命令行参数
program.parse(process.argv);
