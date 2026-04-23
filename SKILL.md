# Send to Kindle

## 触发条件
- **指令触发**: `/Kindle` 或 `@Kindle`（后跟内容或文件路径）
- **自然语言**: 用户说"发送电子书"、"发到 Kindle"、"推送到 Kindle"、"Send to Kindle"
- **文件触发**: 用户发送了一个文档文件（如 pdf, epub, docx, mobi 等）并要求发送到 Kindle

## 功能描述
将用户指定的文档转换为 TXT 格式，并通过电子邮件发送到用户的 Kindle 个人文档服务邮箱。

## 前置依赖
1. **pandoc**: 文档转换工具
   ```bash
   sudo apt install pandoc
   ```
2. **环境变量配置** (必须配置发件人邮箱信息):
   ```bash
   export KINDLE_SENDER_EMAIL="your_email@example.com"
   export KINDLE_SENDER_PASSWORD="your_app_password"  # 注意：通常是授权码而非登录密码
   export KINDLE_SMTP_SERVER="smtp.qq.com"           # 根据你的邮箱服务商修改
   export KINDLE_SMTP_PORT="465"
   ```

## 使用方法
### 命令行
```bash
python send_to_kindle.py /path/to/document.pdf
```

### 自动化流程
1. 识别用户意图（发送文档到 Kindle）。
2. 获取文档路径（如果是微信/Telegram 发送的文件，先下载到本地）。
3. 执行脚本进行转换和发送。
4. 向用户反馈发送状态。

## 注意事项
- Kindle 个人文档服务支持多种格式，但本技能统一转为 TXT 以确保最佳兼容性。
- 发件人邮箱必须在 Amazon Kindle 的"已认可的发件人电子邮箱列表"中已添加。
- 使用 QQ 邮箱、163 邮箱等时，`KINDLE_SENDER_PASSWORD` 应填写**授权码**，而非登录密码。

## 脚本路径
`/home/nanobot/.nanobot/skills/sendtokindle/send_to_kindle.py`
