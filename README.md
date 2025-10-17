# TinySSH

一个用 Go 编写的轻量级 SSH 服务端，支持从 JSON 配置读取监听信息、账户密码、主机密钥路径等，并可配合 systemd 管理运行。

## 功能特性

- JSON 配置（监听地址/端口、Shell、账户密码、主机密钥路径）
- 首次启动自动生成 RSA 主机密钥，之后复用
- 基于用户名/密码的认证，常量时间比较
- 支持 PTY、环境变量、窗口大小调整、`exec` 与交互 `shell`
- 结构化日志（`slog`），可通过 `-log-level` 调整
- 提供 systemd 单元文件，方便部署为守护进程

## 快速开始

```
curl -o /etc/systemd/system/tinyssh.service https://raw.githubusercontent.com/dollarkillerx/tinyssh/refs/heads/main/packaging/systemd/tinyssh.service 
curl -o /opt/tinyssh/tinyssh https://github.com/dollarkillerx/tinyssh/releases/download/v0.0.1/tinyssh

systemctl start tinyssh.service
```

## 快速开始

1. 克隆本仓库并安装 Go 1.21+。
2. 下载依赖并构建：

   ```bash
   go mod tidy
   go build -o tinyssh ./cmd/tinyssh
   ```

3. 复制示例配置并修改：

   ```bash
   cp config.example.json config.json
   # 根据需要调整监听端口、用户密码、shell、host_key_path 等
   ```

4. 运行服务：

   ```bash
   ./tinyssh -config config.json -log-level debug
   ```

5. 使用 SSH 客户端连接：

   ```bash
   ssh demo@127.0.0.1 -p 2222
   ```

   首次连接会提示主机指纹确认，输入配置中的用户名/密码即可。

## 配置说明

`config.json` 关键字段：

- `listen_address`：监听地址，支持 `"0.0.0.0:2222"`、`":2222"` 等形式。若留空会根据 `listen_port` 自动补全。
- `listen_port`：可选；仅在 `listen_address` 未设置时作为端口使用。
- `host_key_path`：服务器私钥（Host Key）保存位置。若文件不存在会自动生成；需确保可写且为具体文件路径。
- `shell`：登录后启动的交互 Shell，可设为 `/bin/sh`、`/bin/bash`、`/bin/zsh` 等。留空时使用进程环境变量 `SHELL`，再无则默认 `/bin/sh`。
- `users`：用户名/密码列表，至少配置一个账户。

## systemd 部署

1. 编译后的二进制复制到 `/usr/local/bin/tinyssh`。
2. 创建配置目录并放置配置：

   ```bash
   sudo mkdir -p /etc/tinyssh
   sudo cp config.json /etc/tinyssh/config.json
   sudo chown -R tinyssh:tinyssh /etc/tinyssh
   ```

3. 创建运行用户与组：

   ```bash
   sudo useradd --system --no-create-home --shell /usr/sbin/nologin tinyssh
   ```

4. 安装 systemd 单元文件：

   ```bash
   sudo cp packaging/systemd/tinyssh.service /etc/systemd/system/tinyssh.service
   sudo systemctl daemon-reload
   sudo systemctl enable --now tinyssh
   ```

5. 查看运行状态与日志：

   ```bash
   sudo systemctl status tinyssh
   journalctl -u tinyssh -f
   ```

## 调试与排错

- 若 shell 请求失败，请检查 `shell` 字段是否指向存在且可执行的二进制；启动时使用 `-log-level debug` 可看到详细错误。
- 如果主机密钥路径配置为目录，启动会报错 `read host key: is a directory`，需改成具体文件。
- `go mod tidy` / `go build` 若因网络受限失败，可预先下载依赖或在有网络的环境运行后同步依赖目录。

## 许可

本项目遵循 [MIT License](LICENSE)。
