package ssh

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// DirEntry 目录条目
type DirEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

// whitespaceRegex 预编译的正则表达式
var whitespaceRegex = regexp.MustCompile(`\s+`)

type Config struct {
	Host                 string
	Port                 string
	User                 string
	KeyPath              string
	BasePath             string
	FilenamePrefixLength int
}

type Client struct {
	config     *Config
	sshClient  *ssh.Client
	netConn    net.Conn     // 底层 TCP 连接，用于强制关闭
	connMu     sync.Mutex // 保护重连逻辑
	homeDir    string     // 缓存的远程家目录
}

func NewClient(config *Config) *Client {
	if config.Port == "" {
		config.Port = "22"
	}
	if config.BasePath == "" {
		config.BasePath = "~/knas_archive"
	}

	return &Client{
		config: config,
	}
}

func (c *Client) Connect() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.connectLocked()
}

// connectLocked 建立 SSH 连接（调用方需持有 connMu）
func (c *Client) connectLocked() error {
	log.Printf("[INFO] Connecting to %s@%s:%s", c.config.User, c.config.Host, c.config.Port)

	key, err := os.ReadFile(c.config.KeyPath)
	if err != nil {
		return fmt.Errorf("unable to read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("unable to parse private key: %w", err)
	}

	// 创建 known_hosts 文件路径
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	knownHostsPath := filepath.Join(homeDir, ".knas", "known_hosts")

	// 确保 .knas 目录存在
	if err := os.MkdirAll(filepath.Join(homeDir, ".knas"), 0755); err != nil {
		return fmt.Errorf("failed to create .knas directory: %w", err)
	}

	// 创建 known_hosts 回调函数
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		// 如果文件不存在或格式错误，创建一个新的
		if os.IsNotExist(err) || strings.Contains(err.Error(), "unknown key") {
			// 使用一个宽松的回调来首次连接，然后保存主机密钥
			hostKeyCallback = ssh.HostKeyCallback(func(hostname string, remote net.Addr, key ssh.PublicKey) error {
				khPath := knownHostsPath
				line := knownhosts.Line([]string{hostname}, key)
				f, err := os.OpenFile(khPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					return fmt.Errorf("failed to open known_hosts: %w", err)
				}
				defer f.Close()
				if _, err := f.WriteString(line + "\n"); err != nil {
					return fmt.Errorf("failed to write to known_hosts: %w", err)
				}
				log.Printf("[INFO] Added host key for %s to known_hosts", hostname)
				return nil
			})
		} else {
			return fmt.Errorf("failed to create known_hosts callback: %w", err)
		}
	}

	config := &ssh.ClientConfig{
		User: c.config.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%s", c.config.Host, c.config.Port)

	// 先建立 TCP 连接，保存引用以便强制关闭底层 socket
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create SSH connection: %w", err)
	}

	c.sshClient = ssh.NewClient(sshConn, chans, reqs)
	c.netConn = conn

	// 解析远程家目录（用于 expandPath）
	if err := c.resolveRemoteHome(); err != nil {
		log.Printf("[WARN] Failed to resolve remote home: %v", err)
	}

	log.Println("[INFO] SSH connection established")
	return nil
}

func (c *Client) Disconnect() error {
	if c.sshClient != nil {
		err := c.sshClient.Close()
		c.sshClient = nil
		if c.netConn != nil {
			c.netConn.Close()
			c.netConn = nil
		}
		return err
	}
	return nil
}

// ForceReset 强制断开所有连接，确保下次操作触发全新重连
func (c *Client) ForceReset() {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.netConn != nil {
		c.netConn.Close()
		c.netConn = nil
	}
	if c.sshClient != nil {
		c.sshClient.Close()
		c.sshClient = nil
	}
}

// ensureConnected 检查连接存活性，断线时自动重连
func (c *Client) ensureConnected() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.sshClient != nil {
		// 给 keepalive 探活设置超时，避免在僵死连接上无限等待
		if c.netConn != nil {
			c.netConn.SetDeadline(time.Now().Add(5 * time.Second))
		}
		_, _, err := c.sshClient.SendRequest("keepalive@openssh.com", true, nil)
		if c.netConn != nil {
			c.netConn.SetDeadline(time.Time{}) // 清除 deadline
		}
		if err == nil {
			return nil
		}
		// 连接已死，强制关闭底层 TCP 连接
		if c.netConn != nil {
			c.netConn.Close()
			c.netConn = nil
		}
		c.sshClient.Close()
		c.sshClient = nil
		log.Println("[WARN] SSH connection lost, reconnecting...")
	}

	return c.connectLocked()
}

// resolveRemoteHome 通过 SSH 获取远程真实家目录并缓存
func (c *Client) resolveRemoteHome() error {
	if c.homeDir != "" {
		return nil
	}
	session, err := c.sshClient.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	output, err := session.Output("echo ~")
	if err != nil {
		return err
	}
	c.homeDir = strings.TrimSpace(string(output))
	log.Printf("[INFO] Remote home directory: %s", c.homeDir)
	return nil
}

func (c *Client) expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if c.homeDir != "" {
			return c.homeDir + path[1:]
		}
		// 回退：无法解析远程家目录时使用默认路径
		return "/home/" + c.config.User + path[1:]
	}
	if path == "~" {
		if c.homeDir != "" {
			return c.homeDir
		}
		return "/home/" + c.config.User
	}
	return path
}

// shellEscape 安全转义路径，防止空格断裂或 Shell 注入
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (c *Client) MkdirAll(path string) error {
	if err := c.ensureConnected(); err != nil {
		return fmt.Errorf("reconnect failed: %w", err)
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	fullPath := c.expandPath(path)
	cmd := fmt.Sprintf("mkdir -p %s", shellEscape(fullPath))

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	err = session.Run(cmd)
	if err != nil {
		return fmt.Errorf("failed to create directory: %s, stderr: %s", err, stderr.String())
	}

	return nil
}

func (c *Client) WriteFile(path string, content string) error {
	if err := c.ensureConnected(); err != nil {
		return fmt.Errorf("reconnect failed: %w", err)
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	fullPath := c.expandPath(path)
	// 使用 cat 命令写入文件，避免特殊字符问题
	cmd := fmt.Sprintf("cat > %s", shellEscape(fullPath))

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	if _, err := fmt.Fprint(stdin, content); err != nil {
		return fmt.Errorf("failed to write to stdin: %w", err)
	}

	if err := stdin.Close(); err != nil {
		return fmt.Errorf("failed to close stdin: %w", err)
	}

	if err := session.Wait(); err != nil {
		return fmt.Errorf("command failed: %s, stderr: %s", err, stderr.String())
	}

	return nil
}

// contentHash 返回内容的 MD5 哈希十六进制串
func contentHash(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])
}

// ExistsByHash 检查远程当天目录中是否已存在包含指定哈希的文件
func (c *Client) ExistsByHash(relPath, hash string) bool {
	if err := c.ensureConnected(); err != nil {
		return false
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return false
	}
	defer session.Close()

	dirPath := c.expandPath(filepath.Join(c.config.BasePath, relPath))
	// 用 grep -rl 在当天目录中搜索包含哈希标记的文件
	cmd := fmt.Sprintf("grep -rl 'content_hash: %s' %s 2>/dev/null | head -1", hash, shellEscape(dirPath))

	output, err := session.Output(cmd)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

func (c *Client) SyncItem(content string, timestamp time.Time) (string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", fmt.Errorf("reconnect failed: %w", err)
	}

	year := timestamp.Format("2006")
	month := timestamp.Format("01")
	day := timestamp.Format("02")
	timeStr := timestamp.Format("150405")

	// 计算内容哈希，用于去重
	hash := contentHash([]byte(content))
	relPath := filepath.Join(year, month, day)

	// 检查远程当天目录中是否已有相同内容
	if c.ExistsByHash(relPath, hash) {
		log.Printf("[INFO] Duplicate content skipped (hash: %s)", hash[:8])
		return "", nil
	}

	// 从配置获取前缀长度，默认为 20
	prefixLength := c.config.FilenamePrefixLength
	if prefixLength == 0 {
		prefixLength = 20
	}

	// 提取前 N 个字符作为文件名
	prefix := extractContentPrefix(content, prefixLength)

	fileName := fmt.Sprintf("%s_%s.md", timeStr, prefix)
	fullPath := filepath.Join(c.config.BasePath, relPath, fileName)

	// 创建目录
	if err := c.MkdirAll(filepath.Join(c.config.BasePath, relPath)); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// 准备文件内容（包含 content_hash 用于后续去重）
	fileContent := c.formatContent(content, timestamp, hash)

	// 写入文件
	if err := c.WriteFile(fullPath, fileContent); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	log.Printf("[INFO] Synced to remote: %s", fullPath)
	return fullPath, nil
}

// extractContentPrefix 提取内容的前 n 个字符，清理特殊字符
func extractContentPrefix(content string, n int) string {
	// 清理内容：移除空白字符、换行等
	content = strings.TrimSpace(content)
	content = whitespaceRegex.ReplaceAllString(content, " ")

	// 提取前 n 个字符
	runes := []rune(content)
	if len(runes) > n {
		runes = runes[:n]
	}

	// 只保留字母、数字、中文和常见符号
	var result []rune
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) ||
			r == '_' || r == '-' || r == ' ' {
			result = append(result, r)
		}
	}

	prefix := string(result)
	// 移除空格
	prefix = strings.ReplaceAll(prefix, " ", "_")
	prefix = strings.ReplaceAll(prefix, "/", "_")
	prefix = strings.ReplaceAll(prefix, "\\", "_")

	if len(prefix) == 0 {
		return "untitled"
	}

	return prefix
}

func (c *Client) formatContent(content string, timestamp time.Time, hash string) string {
	return fmt.Sprintf(`---
sync_time: %s
source: clipboard
content_hash: %s
---

%s`,
		timestamp.Format("2006-01-02 15:04:05"),
		hash,
		content)
}

func (c *Client) TestConnection() error {
	if err := c.ensureConnected(); err != nil {
		return fmt.Errorf("reconnect failed: %w", err)
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.Output("echo 'connection test'")
	if err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}

	if strings.TrimSpace(string(output)) != "connection test" {
		return fmt.Errorf("unexpected output: %s", output)
	}

	return nil
}

// SyncImage 同步图片到远程服务器
func (c *Client) SyncImage(data []byte, timestamp time.Time) (string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", fmt.Errorf("reconnect failed: %w", err)
	}

	year := timestamp.Format("2006")
	month := timestamp.Format("01")
	day := timestamp.Format("02")
	timeStr := timestamp.Format("150405")

	// 计算图片哈希用于去重
	hash := contentHash(data)
	relPath := filepath.Join(year, month, day)

	// 检查远程当天目录中是否已有相同图片（通过文件名中的哈希前缀判断）
	if c.imageExistsByHash(relPath, hash[:8]) {
		log.Printf("[INFO] Duplicate image skipped (hash: %s)", hash[:8])
		return "", nil
	}

	// 图片文件名中包含哈希前8位，用于后续去重判断
	fileName := fmt.Sprintf("%s_%s_image.png", timeStr, hash[:8])
	fullPath := filepath.Join(c.config.BasePath, relPath, fileName)

	// 创建目录
	if err := c.MkdirAll(filepath.Join(c.config.BasePath, relPath)); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// 写入图片
	if err := c.WriteBinary(fullPath, data); err != nil {
		return "", fmt.Errorf("failed to write image: %w", err)
	}

	log.Printf("[INFO] Synced image to remote: %s", fullPath)
	return fullPath, nil
}

// imageExistsByHash 检查远程当天目录中是否存在包含指定哈希前缀的图片文件
func (c *Client) imageExistsByHash(relPath, hashPrefix string) bool {
	if err := c.ensureConnected(); err != nil {
		return false
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return false
	}
	defer session.Close()

	dirPath := c.expandPath(filepath.Join(c.config.BasePath, relPath))
	cmd := fmt.Sprintf("ls %s/*_%s_image.png 2>/dev/null | head -1", shellEscape(dirPath), hashPrefix)

	output, err := session.Output(cmd)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// ReadFile 从远程服务器读取文件的二进制内容
func (c *Client) ReadFile(path string) ([]byte, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, fmt.Errorf("reconnect failed: %w", err)
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	fullPath := c.expandPath(path)
	cmd := fmt.Sprintf("cat %s", shellEscape(fullPath))

	output, err := session.Output(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return output, nil
}

// WriteBinary 二进制安全写入
func (c *Client) WriteBinary(path string, data []byte) error {
	if err := c.ensureConnected(); err != nil {
		return fmt.Errorf("reconnect failed: %w", err)
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	fullPath := c.expandPath(path)
	cmd := fmt.Sprintf("cat > %s", shellEscape(fullPath))

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("failed to write binary data: %w", err)
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("failed to close stdin: %w", err)
	}

	if err := session.Wait(); err != nil {
		return fmt.Errorf("command failed: %s, stderr: %s", err, stderr.String())
	}

	return nil
}

// ListDir 列出远程目录内容
func (c *Client) ListDir(remotePath string) ([]DirEntry, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, fmt.Errorf("reconnect failed: %w", err)
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	fullPath := c.expandPath(remotePath)
	cmd := fmt.Sprintf("ls -la --time-style=long-iso %s", shellEscape(fullPath))

	output, err := session.Output(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list directory: %w", err)
	}

	var entries []DirEntry
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}

		name := strings.Join(fields[7:], " ")
		if name == "." || name == ".." || strings.HasPrefix(name, ".") || name == "@eaDir" {
			continue
		}

		perms := fields[0]
		isDir := len(perms) > 0 && perms[0] == 'd'

		var size int64
		fmt.Sscanf(fields[4], "%d", &size)

		modTime := fields[5] + " " + fields[6]

		entries = append(entries, DirEntry{
			Name:    name,
			IsDir:   isDir,
			Size:    size,
			ModTime: modTime,
		})
	}

	return entries, nil
}

// SearchResult 搜索结果条目
type SearchResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Line    int    `json:"line"`
}

// Search 在远程归档目录中全文搜索
func (c *Client) Search(keyword string, limit int) ([]SearchResult, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, fmt.Errorf("reconnect failed: %w", err)
	}

	session, err := c.sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	if limit <= 0 {
		limit = 50
	}

	basePath := c.expandPath(c.config.BasePath)
	// grep -rn: 递归搜索，显示行号，匹配结果限制条数
	cmd := fmt.Sprintf(
		"grep -rn --include='*.md' --include='*.txt' --max-count=%d %s %s 2>/dev/null",
		limit, shellEscape(keyword), shellEscape(basePath),
	)

	output, err := session.Output(cmd)
	if err != nil {
		// grep 返回非零表示没匹配到，不算错误
		if strings.Contains(err.Error(), "exit status 1") {
			return nil, nil
		}
		return nil, fmt.Errorf("search failed: %w", err)
	}

	var results []SearchResult
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// 格式: /path/to/file:lineno:matched content
		idx1 := strings.Index(line, ":")
		if idx1 < 0 {
			continue
		}
		idx2 := strings.Index(line[idx1+1:], ":")
		if idx2 < 0 {
			continue
		}
		filePath := line[:idx1]
		lineNumStr := line[idx1+1 : idx1+1+idx2]
		content := line[idx1+1+idx2+1:]

		// 转为相对路径
		relPath := strings.TrimPrefix(filePath, basePath+"/")

		var lineNum int
		fmt.Sscanf(lineNumStr, "%d", &lineNum)

		results = append(results, SearchResult{
			Path:    relPath,
			Content: content,
			Line:    lineNum,
		})
		if len(results) >= limit {
			break
		}
	}

	return results, nil
}
