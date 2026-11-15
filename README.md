# WS Relay

任务中转服务器：执行端通过 WebSocket 连接并用 UID 标识，发送端通过 HTTP 把任务发给指定 UID，服务器转发并把执行端的回复原样返回给发送端。

- 单个可执行文件，无数据库、无运行时依赖，网页面板已打包在内
- 同 UID 已在线时拒绝新连接（僵尸连接例外，见下文）
- 目标离线立即告知发送方；执行中途断线立即返回，不用等超时
- 违规 3 次自动拉黑（IP 和 UID 分别计数），黑名单存在 `data/bans.json`
- 面板 `/panel`：在线执行端、违规记录、黑名单、踢人/拉黑/解封

协议细节见 [docs/PROTOCOL.md](docs/PROTOCOL.md)。执行端 SDK 在 `sdk/`（JS、Python，都不需要装依赖）。

## 目录

```
main.go      启动、信号处理
config.go    config.json 读取与默认值
hub.go       UID→连接、任务派发与等待回复
server.go    WS 接入、HTTP 接口、违规判定
guard.go     违规计数、黑名单（bans.json）
panel.go     面板接口；panel.html 面板页面
sdk/         执行端示例
```

以后改违规规则只需要看 `server.go` 里调用 `violate` 的地方。

## 宝塔部署

### 1. 构建

在任意装了 Go 1.24+ 的机器上：

```bash
./build.sh 1.0.0
```

得到 `dist/ws-relay-1.0.0-linux-amd64.tar.gz`。服务器是 ARM 的话用 `ARCH=arm64 ./build.sh 1.0.0`。

### 2. 上传

解压后把 `ws-relay` 上传到 `/www/wwwroot/ws-relay/`，在宝塔文件管理里把权限设为 `755`。

### 3. 添加 Go 项目

宝塔 → 网站 → Go项目 → 添加：

| 项 | 填写 |
|---|---|
| 项目执行文件 | `/www/wwwroot/ws-relay/ws-relay` |
| 项目端口 | `8080` |
| 执行命令 | `/www/wwwroot/ws-relay/ws-relay -config /www/wwwroot/ws-relay/config.json` |
| 运行用户 | `www` |
| 开机启动 | 勾选 |
| 绑定域名 | 你的域名 |

首次启动会生成 `config.json`，面板口令在项目日志里，也可以直接打开 `config.json` 看 `panel_password`。

### 4. 检查反向代理配置（重要）

打开该站点的配置文件（网站 → 设置 → 配置文件），确认反代 `location /` 里有下面几行，没有就补上：

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection "upgrade";
proxy_set_header Host $host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_read_timeout 300s;
```

- 前三行缺了 WebSocket 连不上。
- `X-Real-IP` 缺了，服务器看到的 IP 全是 127.0.0.1。回环地址不会被拉黑，但这样一来 IP 拉黑也就完全不起作用了。
- `proxy_read_timeout` 要大于心跳间隔，同时要大于 HTTP 任务的最长等待时间（`max_task_timeout`，默认 120 秒）。

### 5. 访问

- 面板：`http://你的域名/panel`
- 执行端：`ws://你的域名/ws`（开了 SSL 就用 `wss://`）
- 发送端：`POST http://你的域名/api/v1/task`

默认监听 `127.0.0.1:8080`，只有 nginx 能访问，**不要**在宝塔安全里放行 8080。

## 升级

1. 在本地构建新版本。
2. 在宝塔里停止项目，用新的 `ws-relay` 替换旧文件，再启动。
3. `config.json` 和 `data/bans.json` 都会保留。执行端会断开几秒，SDK 会自动重连。
4. `/healthz` 返回当前版本号，可以用来确认升级是否生效。

配置新增字段时不需要手动改：缺少的字段会自动使用默认值。

## 配置说明

```json
{
  "listen_addr": "127.0.0.1:8080",
  "token": "",
  "panel_password": "首次运行随机生成",
  "trust_proxy": true,
  "task_timeout": 30,
  "max_task_timeout": 120,
  "heartbeat": 30,
  "max_message_bytes": 65536,
  "violation_limit": 3,
  "violation_window": 10,
  "ban_minutes": 0,
  "data_dir": "data"
}
```

| 字段 | 说明 |
|---|---|
| `token` | 共享 Token，留空表示不校验。执行端在注册消息里带上，发送端放在参数或 `X-Token` 头里 |
| `trust_proxy` | 从 nginx 传来的头里取真实 IP。服务器直接对外、没有 nginx 时**必须**设为 `false`，否则别人可以伪造 IP |
| `task_timeout` / `max_task_timeout` | HTTP 等待回复的默认秒数和上限秒数，调用方可以用 `timeout` 参数自行指定 |
| `heartbeat` | 服务器发 ping 的间隔。超过 2.5 倍间隔没收到执行端任何消息就断开 |
| `violation_limit` / `violation_window` | 在 `violation_window` 分钟内违规达到 `violation_limit` 次就拉黑 |
| `ban_minutes` | 自动拉黑的时长（分钟），0 表示永久 |

修改配置后需要重启项目才会生效。

## 同 UID 规则

同一个 UID 已经在线时，新连接会收到 `uid_in_use` 并被拒绝。

例外：如果旧连接已经超过 `heartbeat + 10` 秒（默认 40 秒）没有发过任何消息，就判定它是掉线后还没被发现的僵尸连接，由新连接接管。这样执行端断网重连时不会被自己的旧连接一直挡在外面。

## 本地开发

```bash
go test ./...
go run . -config config.json
node sdk/client.js ws://127.0.0.1:8080/ws js_demo
python3 sdk/client.py ws://127.0.0.1:8080/ws py_demo
```
