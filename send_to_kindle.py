#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Send to Kindle Skill (Refactored)
1. Extracts title from the first line of content/file.
2. Renames file to '雨轩-{Title}.txt'.
3. Sends to Kindle via email.
"""

import os
import sys
import subprocess
import smtplib
import tempfile
import re
import shutil
import datetime
from email.mime.multipart import MIMEMultipart
from email.mime.text import MIMEText
from email.mime.base import MIMEBase
from email import encoders
from pathlib import Path

# --- Configuration ---
def load_env(env_path):
    if os.path.exists(env_path):
        with open(env_path) as f:
            for line in f:
                if line.strip() and not line.startswith('#'):
                    key, value = line.strip().split('=', 1)
                    os.environ.setdefault(key, value)

script_dir = Path(__file__).parent.resolve()
load_env(script_dir / '.env')

SENDER_EMAIL = os.getenv("KINDLE_SENDER_EMAIL")
SENDER_PASSWORD = os.getenv("KINDLE_SENDER_PASSWORD")
SMTP_SERVER = os.getenv("KINDLE_SMTP_SERVER", "smtp.qq.com")
SMTP_PORT = int(os.getenv("KINDLE_SMTP_PORT", "465"))
KINDLE_EMAIL = os.getenv("KINDLE_EMAIL", "YUANGUANGSHAN_DSIXMY@kindle.com")

# --- Helper Functions ---

def extract_title(file_path, max_len=20):
    """Read the first non-empty line of a file to use as title."""
    try:
        with open(file_path, 'r', encoding='utf-8') as f:
            for line in f:
                line = line.strip()
                if line:
                    # Remove Markdown headers if present
                    if line.startswith("# "):
                        line = line[2:]
                    elif line.startswith("## "):
                        line = line[3:]
                    
                    # Sanitize filename (remove illegal chars)
                    safe_title = re.sub(r'[\\/*?:"<>|]', "", line)
                    
                    # Truncate if too long
                    if len(safe_title) > max_len:
                        safe_title = safe_title[:max_len]
                        
                    return f"雨轩-{safe_title}"
    except Exception:
        pass
    # Fallback
    return f"雨轩-unknown_{int(datetime.datetime.now().timestamp())}"

def rename_with_title(file_path):
    """Rename a file based on its content title."""
    title = extract_title(file_path)
    new_path = Path(tempfile.gettempdir()) / f"{title}.txt"
    
    # If already in temp dir with correct name, just return
    if file_path.resolve() == new_path.resolve():
        return file_path
        
    try:
        shutil.move(str(file_path), str(new_path))
        return new_path
    except Exception as e:
        print(f"⚠️ Rename failed: {e}")
        return file_path

def check_pandoc():
    try:
        subprocess.run(["pandoc", "--version"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False

def convert_to_txt(input_path):
    """Convert document to TXT using pandoc."""
    if not check_pandoc():
        print("❌ Error: pandoc is not installed.")
        sys.exit(1)

    # Convert to a temporary file first
    temp_txt = Path(tempfile.gettempdir()) / f"temp_converted_{os.getpid()}.txt"
    
    try:
        subprocess.run(
            ["pandoc", str(input_path), "-o", str(temp_txt)],
            check=True,
            capture_output=True,
            text=True
        )
    except subprocess.CalledProcessError as e:
        print(f"❌ Conversion failed: {e.stderr}")
        sys.exit(1)

    # Rename based on title
    return rename_with_title(temp_txt)

def process_text(text_content):
    """Save text content to a temp file and rename with title."""
    # 1. Save raw text
    temp_path = Path(tempfile.gettempdir()) / f"temp_text_{os.getpid()}.txt"
    with open(temp_path, 'w', encoding='utf-8') as f:
        f.write(text_content)
    
    # 2. Rename with title
    return rename_with_title(temp_path)

def send_email(file_path):
    """Send email with attachment."""
    if not SENDER_EMAIL or not SENDER_PASSWORD:
        print("❌ Error: Missing email credentials.")
        sys.exit(1)

    msg = MIMEMultipart()
    msg["From"] = SENDER_EMAIL
    msg["To"] = KINDLE_EMAIL
    msg["Subject"] = file_path.stem  # Use filename as subject

    msg.attach(MIMEText("Sent by nanobot sendtokindle skill.", "plain"))

    with open(file_path, "rb") as attachment:
        part = MIMEBase("application", "octet-stream")
        part.set_payload(attachment.read())
    
    encoders.encode_base64(part)
    part.add_header("Content-Disposition", "attachment", filename=file_path.name)
    msg.attach(part)

    try:
        server = smtplib.SMTP_SSL(SMTP_SERVER, SMTP_PORT)
        server.login(SENDER_EMAIL, SENDER_PASSWORD)
        server.sendmail(SENDER_EMAIL, KINDLE_EMAIL, msg.as_string())
        server.quit()
        print(f"✅ Successfully sent {file_path.name}")
    except Exception as e:
        print(f"❌ Failed to send: {e}")
        sys.exit(1)

# --- Main Logic ---

def main():
    if len(sys.argv) < 2:
        print("Usage: python send_to_kindle.py <file_path or text_content>")
        sys.exit(1)

    input_arg = sys.argv[1]
    input_path = Path(input_arg)
    
    print(f"📥 Received input: {input_arg[:50]}...")

    final_path = None

    # Determine if input is a file or raw text
    # Check length first to avoid OSError on long text arguments
    is_path = False
    try:
        if len(input_arg) < 1000:  # Reasonable path length limit
            is_path = input_path.exists() and input_path.is_file()
    except OSError:
        is_path = False

    if is_path:
        print(f"📄 Detected file: {input_path}")
        if input_path.suffix.lower() == ".txt":
            # If already txt, just copy to temp and rename with title
            temp_txt = Path(tempfile.gettempdir()) / f"temp_copy_{os.getpid()}.txt"
            shutil.copy2(input_path, temp_txt)
            final_path = rename_with_title(temp_txt)
        else:
            # Convert non-txt file
            final_path = convert_to_txt(input_path)
    else:
        # Raw text
        print(f"📝 Detected text content...")
        final_path = process_text(input_arg)

    if final_path and final_path.exists():
        send_email(final_path)
        # Cleanup
        final_path.unlink()
        print(f"🧹 Cleaned up: {final_path}")
    else:
        print("❌ Processing failed.")
        sys.exit(1)

if __name__ == "__main__":
    main()
