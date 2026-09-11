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

// backfillBatch 回溯时每批聚合的文档数，平衡事务开销与写入锁占用时间。
const backfillBatch = 200

// batchChunkFiles 是单次 tar 批量读取的最大文件数。超大目录（如 uploads
// 1.6 万文件）一次打包会产生数百 MB 流，超出 SSH 缓冲且解析内存峰值过高。
const batchChunkFiles = 400

// batchChunkBytes 是单块累计正文体积上限。仅按文件数分块不够：归档月份目录
// 单文件约 9KB（400 个才 3.6MB），而 uploads 单文件平均约 900KB（400 个即
// 367MB，session.Output 会在内存里留存整块，再乘上解包 map 就是 700MB+）。
// 按体积封顶后大文件目录自动切成小块，内存峰值恒定在数十 MB。
const batchChunkBytes = 64 << 20

// readDirContents 读取目录下指定文件的内容：优先 tar 批量读（一次 SSH 往返
// 读一块），整块失败时降级为逐文件读，保证不因个别坏文件丢失整目录。
// sizeOf 用于按体积分块（缺省视为 0，则退化为仅按文件数分块）。
func readDirContents(sc SSHClient, dir string, names []string, sizeOf map[string]int64) (map[string][]byte, error) {
	out := make(map[string][]byte, len(names))
	var firstErr error

	for start := 0; start < len(names); {
		end := start
		var bytes int64
		for end < len(names) && end-start < batchChunkFiles {
			sz := sizeOf[names[end]]
			// 至少收一个文件，避免单个超大文件把分块卡成死循环。
			if end > start && bytes+sz > batchChunkBytes {
				break
			}
			bytes += sz
			end++
		}
		chunk := names[start:end]
		start = end

		contents, err := sc.ReadFilesBatch(dir, chunk)
		if err == nil {
			for k, v := range contents {
				out[k] = v
			}
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		// 降级：逐文件读这一块（慢，但只针对失败块，且不会丢数据）
		log.Printf("[WARN] backfill batch chunk failed at %s (%d files), falling back to per-file: %v",
			dir, len(chunk), err)
		for _, name := range chunk {
			data, ferr := sc.ReadFile(filepath.Join(dir, name))
			if ferr != nil {
				continue
			}
			out[name] = data
		}
	}
	return out, firstErr
}

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
// 写入采用批量聚合（每 backfillBatch 条一次性 BulkIndex），避免逐条事务带来的
// 大量锁竞争与提交开销；同时串行化写入由 index.Index 的互斥锁保证。
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
	batch := make([]index.Doc, 0, backfillBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := ix.BulkIndex(batch); err != nil {
			log.Printf("[WARN] backfill batch: %v", err)
			return
		}
		count += len(batch)
		log.Printf("[INFO] backfill %s: flushed %d (total %d)", dir, len(batch), count)
		batch = batch[:0]
		// 每批写后合并 WAL：防巨型 WAL 导致 checkpoint 变成 CPU 黑洞
		// （200MB WAL 时 backfill 实测卡死）。
		ix.Checkpoint()
	}

	// 先收集本目录下所有 .md/.txt 文件名，然后一次性批量读取整批文件。
	// 逐文件 ReadFile 会让每个文件新建一次 SSH session（46k 文件 = 46k 次往返），
	// 是本回填此前慢到不可用的根因；批量读把往返降到目录数量级。
	var names []string
	sizeOf := make(map[string]int64, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		sizeOf[e.Name] = e.Size
		if strings.HasSuffix(e.Name, ".md") || strings.HasSuffix(e.Name, ".txt") {
			names = append(names, e.Name)
		}
	}
	if len(names) > 0 {
		dirStart := time.Now()
		contents, err := readDirContents(sc, dir, names, sizeOf)
		if err != nil {
			// 已降级逐文件读过，这里只提示哪块批量失败（非致命）
			log.Printf("[INFO] backfill %s: some batches fell back to per-file reads: %v", dir, err)
		}
		// 进度可观测性：大目录回填慢时能看出卡在哪个目录、读了多少文件耗时多少
		if len(names) >= 50 || time.Since(dirStart) > 5*time.Second {
			log.Printf("[INFO] backfill dir %s: %d files read in %v (%d ok)",
				dir, len(names), time.Since(dirStart).Round(time.Millisecond), len(contents))
		}
		// 一次性预取该目录已索引的 path 集合（一次 LIKE 查询 vs 每文件一次
		// 全表 GetByPath——后者在大目录 + 大表下慢到不可用）。
		relDir := strings.TrimPrefix(dir, base)
		relDir = strings.TrimPrefix(relDir, "/")
		indexedSet, _ := ix.PathsInDir(relDir)
		var skipped int
		for _, name := range names {
			data, ok := contents[name]
			if !ok {
				continue // 单个文件缺失/不可读：静默跳过，不中断整批
			}
			full := filepath.Join(dir, name)
			rel := strings.TrimPrefix(full, base)
			rel = strings.TrimPrefix(rel, "/")
			// 增量跳过：已索引的 path 不再重复 DELETE+INSERT。FTS5 的删插
			// 会对倒排索引整块重写（13 倍慢于纯插入），且滚动滚大 WAL——
			// WAL 上 200MB 后 checkpoint 变成 CPU 黑洞（线上卡死真凶）。
			// 回填语义是补齐缺口（uploads/、断档月份），已索引内容不变。
			if indexedSet[rel] {
				skipped++
				continue
			}
			title, tags, body := parseFrontmatter(string(data))
			batch = append(batch, index.Doc{
				Path:    rel,
				NasPath: full,
				Title:   title,
				Tags:    tags,
				Type:    "text",
				Time:    parseTimeFromRelPath(rel),
				Content: body,
			})
			if len(batch) >= backfillBatch {
				flush()
			}
		}
		if skipped > 0 {
			log.Printf("[INFO] backfill %s: skipped %d already-indexed entries", dir, skipped)
		}
	}

	// 递归子目录
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		full := filepath.Join(dir, e.Name)
		n, err := walkAndIndex(ctx, sc, ix, full, base)
		count += n
		if err != nil {
			log.Printf("[WARN] backfill dir %s: %v", full, err)
		}
	}
	flush()
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
