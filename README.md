# Simple WS Relay

面向 Windows Server 的纯内存 WebSocket 中转服务器。单文件运行，不使用数据库、不需要Docker、不需要SQLite或其他运行时。

## 功能

- 本地中控、本地客户端和管理员统一连接 `/ws`。
- `X-UID`强制唯一，新连接替换同UID旧连接。
- 配置文件UID白名单，非白名单UID拒绝连接。
- 独立超级管理员Token可在线查询或全量替换白名单。
- 内存记录当前进程中连接过的UID和当前在线UID。
- 三段文本协议完成WS双向转发。
- HTTP一次性API等待目标WS客户端回复。
- 并发HTTP请求使用临时UID关联，不会串包。
- 可配置间隔的普通文本心跳保活，兼容易语言HWA客户端。
- 支持Windows原生服务和开机启动。

完整协议见 [docs/PROTOCOL.md](docs/PROTOCOL.md)，易语言HTTP与WS接入要点见 [docs/EASY_LANGUAGE.md](docs/EASY_LANGUAGE.md)。

## Windows部署

1. 解压发布包到固定目录，例如`C:\WSRelay`。
2. 双击`start.bat`可在控制台运行。首次运行会自动生成带随机256位Token的`config.json`。
3. 安装为服务时，以管理员身份运行PowerShell：

```powershell
cd C:\WSRelay
Set-ExecutionPolicy -Scope Process Bypass
.\install-service.ps1 -OpenFirewall
```

查看服务：

```powershell
.\status.ps1
```

卸载服务但保留配置和日志：

```powershell
.\uninstall-service.ps1
```

## 配置

```json
{
  "listen_addr": ":8080",
  "token": "自动生成的64位十六进制Token",
  "super_admin_token": "单独自动生成的64位十六进制Token",
  "uid_whitelist": ["admin", "controller_001", "client_001"],
  "request_timeout_ms": 10000,
  "heartbeat_interval_seconds": 30,
  "max_message_bytes": 65536,
  "log_dir": "logs",
  "debug": false
}
```

- `heartbeat_interval_seconds`：服务器发送`0-heartbeat-N`文本心跳的间隔，必须大于0，默认30秒。
- `debug`：设为`true`时输出`[WS-DEBUG]`连接、消息和心跳明细；正式运行建议保持`false`。

旧配置中的`ping_interval_seconds`仍可读取，但新字段优先。

环境变量`WS_LISTEN_ADDR`、`WS_TOKEN`、`WS_SUPER_ADMIN_TOKEN`、`WS_LOG_DIR`可以覆盖对应配置。

新安装的白名单默认为空，需要超级管理员调用HTTP `/api`发送`8-8-uid1*uid2`，或者停止服务后直接编辑`uid_whitelist`。`7-7-7`用于查询当前白名单。

从`0.1.x`升级时，需要先在原`config.json`中增加`super_admin_token`和`uid_whitelist`，然后再替换EXE并启动服务。普通Token与超级管理员Token不能相同。

## 无数据库的含义

所有历史UID和在线状态都只存在内存：

- 服务进程重启后历史记录清空。
- 不适合同时运行多个WS服务器实例；不同实例之间不会共享连接或历史。
- 你的SQL系统可以通过管理员WS或HTTP `0-0-0`获取列表并自行持久化。

## 公网安全

该版本按需求只校验`X-Token`，使用明文HTTP/WS。公网链路上的Token和消息可能被截获或篡改。至少应限制防火墙来源IP；正式公网使用仍建议在前面增加TLS反向代理。

## 本地构建

```text
go test ./...
go test -tags=integration ./integration
go build ./cmd/ws-server
```
