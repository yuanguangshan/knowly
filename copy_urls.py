#!/usr/bin/env python3
"""Read blog_url.txt and copy each URL to clipboard with 5-second intervals."""

import subprocess
import sys
import time
from datetime import datetime


def copy_to_clipboard(text: str) -> None:
    """Copy text to macOS system clipboard using pbcopy."""
    subprocess.run(["pbcopy"], input=text.encode("utf-8"), check=True)


def main() -> None:
    file_path = "blog_url.txt"

    with open(file_path, "r", encoding="utf-8") as f:
        urls = [line.strip() for line in f if line.strip()]

    total = len(urls)
    print(f"共 {total} 个 URL，每 5 秒复制一个到剪切板\n")

    for i, url in enumerate(urls, 1):
        copy_to_clipboard(url)

        remaining = total - i
        eta_seconds = remaining * 5
        eta_minutes = eta_seconds // 60
        eta_secs = eta_seconds % 60
        percent = i / total * 100

        print(f"[{i}/{total}] ({percent:.1f}%) 已复制: {url}")
        print(f"  剩余: {remaining} 个 | 预计完成: {eta_minutes}分{eta_secs}秒")

        if i < total:
            # Show countdown
            for countdown in range(5, 0, -1):
                sys.stdout.write(f"\r  {countdown}秒后复制下一个...")
                sys.stdout.flush()
                time.sleep(1)
            print()  # newline after countdown

    print("\n所有 URL 复制完成！")


if __name__ == "__main__":
    main()
