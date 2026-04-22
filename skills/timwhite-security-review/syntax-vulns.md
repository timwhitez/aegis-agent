# 语法与代码级漏洞 (Technical Vulnerabilities)

本文件用于给安全审计 Agent 提供一些常见语法级漏洞模式参考，具体以当前仓库的技术栈为准，自行裁剪。

## 1. 注入攻击 (Injection)

- SQL 注入：检查字符串拼接 SQL 语句。
- 命令注入 (Command Injection)：检查系统命令执行函数。
- 代码注入 (Code Injection)：检查动态代码执行。

## 2. 路径遍历与文件系统操作 (Path Traversal & File IO)

- 文件路径拼接是否允许 `../`、绝对路径或特殊设备路径。
- 临时文件是否写入安全目录，是否可能跳出预期路径。

## 3. 跨站脚本 (XSS)

- 用户输入是否未经转义直接输出到 HTML。
- React / Vue 等场景是否使用危险 API。

## 4. 敏感信息泄露 (Sensitive Data Exposure)

- 硬编码密钥、Token、Password。
- 日志是否泄露手机号、密码、认证 Token、访问凭据、重置链接等。

## 5. 框架与全局安全配置 (Framework & Global Security Configuration)

- 调试 / 开发模式是否在生产中开启。
- CSRF 是否被全局关闭。
- CORS 是否过宽。
- 中间件或统一响应是否削弱认证安全性。

## 6. XML 外部实体 (XXE)

- 检查 XML 解析配置是否禁用了外部实体加载。
