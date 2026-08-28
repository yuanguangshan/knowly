package web

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yuanguangshan/knowly/internal/config"
	"github.com/yuanguangshan/knowly/internal/index"
)

// backfillMu 防止并发回溯（同一时刻只允许一个 backfill 在跑）。
var backfillMu sync.Mutex

// RunBackfill 回溯建索引：递归遍历 NAS 归档目录，把全部 md/txt 文件
// 解析 frontmatter 后灌入本地索引。幂等（同路径先删后插），可随时重复执行。
func RunBackfill(cfg *config.Config, sc SSHClient, ix index.Indexer) {
	if cfg == nil || sc == nil || ix == nil {
		return
	}
	if !backfillMu.TryLock() {
		log.Printf("[INFO] Index backfill already running, skip")
		return
	}
	defer backfillMu.Unlock()

	base := cfg.SSH.BasePath
	start := time.Now()
	log.Printf("[INFO] Index backfill started from %s", base)

	count, err := walkAndIndex(context.Background(), sc, ix, base, base)
	if err != nil {
		log.Printf("[WARN] Index backfill incomplete: %v", err)
	}
	log.Printf("[INFO] Index backfill done: %d entries in %.1fs", count, time.Since(start).Seconds())
}

// walkAndIndex 递归遍历远端目录，索引所有 .md/.txt 文件，返回已索引条数。
func walkAndIndex(ctx context.Context, sc SSHClient, ix index.Indexer, dir, base string) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	entries, err := sc.ListDir(dir)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, e := range entries {
		full := filepath.Join(dir, e.Name)
		if e.IsDir {
			n, err := walkAndIndex(ctx, sc, ix, full, base)
			count += n
			if err != nil {
				log.Printf("[WARN] backfill dir %s: %v", full, err)
			}
			continue
		}
		if !strings.HasSuffix(e.Name, ".md") && !strings.HasSuffix(e.Name, ".txt") {
			continue
		}

		data, err := sc.ReadFile(full)
		if err != nil {
			log.Printf("[WARN] backfill read %s: %v", full, err)
			continue
		}

		rel := strings.TrimPrefix(full, base)
		rel = strings.TrimPrefix(rel, "/")
		title, tags, body := parseFrontmatter(string(data))
		if err := ix.Index(rel, full, title, tags, "text", body, parseTimeFromRelPath(rel)); err != nil {
			log.Printf("[WARN] backfill index %s: %v", rel, err)
			continue
		}
		count++
	}
	return count, nil
}

// parseFrontmatter 从 Markdown 文件中解析 YAML frontmatter 的 title/tags，
// 返回去除 frontmatter 后的正文。
func parseFrontmatter(raw string) (title, tags, body string) {
	raw = strings.TrimPrefix(raw, "\ufeff")
	if !strings.HasPrefix(raw, "---\n") && !strings.HasPrefix(raw, "---\r\n") {
		return "", "", raw
	}
	end := strings.Index(raw[3:], "\n---")
	if end < 0 {
		return "", "", raw
	}
	fm := raw[3 : end+3]
	rest := strings.TrimLeft(raw[end+3:], "\r\n")
	return yamlField(fm, "title"), yamlField(fm, "tags"), rest
}

// yamlField 从 frontmatter 文本中提取 key 对应的值（去引号/方括号，tags 保持 ", " 连接）。
func yamlField(fm, key string) string {
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		v = strings.Trim(v, `"'`)
		if key == "tags" {
			v = strings.TrimPrefix(v, "[")
			v = strings.TrimSuffix(v, "]")
			parts := strings.Split(v, ",")
			for i := range parts {
				parts[i] = strings.Trim(strings.TrimSpace(parts[i]), `"'`)
			}
			v = strings.Join(parts, ", ")
		}
		return v
	}
	return ""
}

// parseTimeFromRelPath 从相对路径 YYYY/MM/DD/HHMMSS_xxx.md 中解析时间；失败回退当前时间。
func parseTimeFromRelPath(rel string) time.Time {
	parts := strings.Split(rel, "/")
	if len(parts) >= 4 && len(parts[3]) >= 6 {
		if t, err := time.Parse("2006/01/02/150405",
			parts[0]+"/"+parts[1]+"/"+parts[2]+"/"+parts[3][:6]); err == nil {
			return t
		}
	}
	return time.Now()
}
