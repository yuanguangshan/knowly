package ssh

import "testing"

// parseLsLine 必须原样保留文件名（尤其连续空格）。
// 回归背景：此前用 strings.Fields 全切再按 " " Join 还原文件名，会把连续空格
// 折叠成一个，导致 uploads 里 186 个文件路径错误 —— tar 报 Cannot stat 退出
// 码 2、整块批量读失败，降级逐文件读也读不到。
func TestParseLsLinePreservesName(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
		dir  bool
		size int64
	}{
		{
			name: "普通文件名",
			line: "-rw-r--r-- 1 root root 2186123 2026-07-23 02:20 022058_斯文在兹.md",
			want: "022058_斯文在兹.md",
			size: 2186123,
		},
		{
			name: "单个空格",
			line: "-rw-r--r-- 1 root root 137 2026-07-23 02:20 GitHub - snapshot.md",
			want: "GitHub - snapshot.md",
			size: 137,
		},
		{
			name: "连续空格",
			line: "-rw-r--r-- 1 root root 4096 2026-07-13 04:10 st-dsv4_context_____-- 知识框架 --__    _knowledge_base.md",
			want: "st-dsv4_context_____-- 知识框架 --__    _knowledge_base.md",
			size: 4096,
		},
		{
			name: "连续空格在中文名中",
			line: "-rw-r--r-- 1 root root 512 2026-07-12 22:56 st-dsv4_文档_ 1__ 提取后_整理__知识   下_今日_1__魏.md",
			want: "st-dsv4_文档_ 1__ 提取后_整理__知识   下_今日_1__魏.md",
			size: 512,
		},
		{
			name: "目录条目",
			line: "drwxr-xr-x 2 root root 4096 2026-07-01 09:00 2026",
			want: "2026",
			dir:  true,
			size: 4096,
		},
		{
			name: "变宽列对齐（属主列更长）",
			line: "-rw-r--r--  1  someuser  somegroup  1234  2026-07-01 09:00 a b  c.md",
			want: "a b  c.md",
			size: 1234,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := parseLsLine(tc.line)
			if !ok {
				t.Fatalf("parseLsLine(%q) 解析失败", tc.line)
			}
			if e.Name != tc.want {
				t.Fatalf("Name = %q; want %q", e.Name, tc.want)
			}
			if e.IsDir != tc.dir {
				t.Fatalf("IsDir = %v; want %v", e.IsDir, tc.dir)
			}
			if e.Size != tc.size {
				t.Fatalf("Size = %d; want %d", e.Size, tc.size)
			}
		})
	}
}

// 字段不足时应判定为无效行，而不是造出错误的条目。
func TestParseLsLineRejectsShortLine(t *testing.T) {
	for _, line := range []string{
		"-rw-r--r-- 1 root root 100",
		"读写 1 root root 100 2026-07-01 09:00",
		"",
	} {
		if _, ok := parseLsLine(line); ok {
			t.Fatalf("parseLsLine(%q) 应判定为无效", line)
		}
	}
}
