# WS 中转协议

## 连接

```http
GET /ws
Upgrade: websocket
X-UID: client_001
X-Type: client
X-Token: <config.json中的共享Token>
```

`X-Type` 允许：

- `controller`：本地中控
- `client`：本地客户端
- `admin`：管理员

UID只允许字母、数字、下划线和点，长度1至128。UID和`txt1`不能包含连字符，因为`-`是协议分隔符。

同UID新连接成功后会立即替换旧连接。管理员不进入客户端清单。

所有WS身份都必须存在于`config.json`的`uid_whitelist`中，否则握手返回HTTP 403和`UID_NOT_ALLOWED`。空白名单禁止所有WS连接。

## 普通WS转发

来源`source_1`发送：

```text
target_1-txt1-txt2-more
```

目标`target_1`收到：

```text
source_1-txt1-txt2-more
```

服务器只分割前两个`-`，所以`txt2`可以继续包含连字符。目标异常时来源收到：

```text
0-error-TARGET_NOT_FOUND
0-error-TARGET_OFFLINE
0-error-INVALID_MESSAGE
```

## 文本心跳

为兼容易语言HWA客户端，服务器不发送WebSocket Ping控制帧，而是定时发送普通文本：

```text
0-heartbeat-1
```

客户端必须立即原样回复同一条文本：

```text
0-heartbeat-1
```

服务器收到匹配序号后保持连接；超时未回复则断开。`0-heartbeat-`为服务器保留前缀，不进入普通UID转发。

## 管理员清单

只有`X-Type: admin`的WS连接可以发送：

```text
0-0-0
```

返回所有在本次服务器进程中成功连接过的中控和客户端：

```text
0-0-client_1,client_2,controller_1
```

发送：

```text
0-0-1
```

返回当前在线中控和客户端：

```text
0-1-client_2,controller_1
```

结果按UID排序并强制去重。历史记录只在内存中，重启服务器后清空。

## HTTP一次性API

```http
POST /api
X-Token: <共享Token>
Content-Type: text/plain

target_1-txt1-txt2
```

服务器生成临时来源UID并向`target_1`发送：

```text
_http.随机请求ID-txt1-txt2
```

目标必须把临时UID作为目标回复：

```text
_http.随机请求ID-result-ok
```

原HTTP请求收到：

```text
target_1-result-ok
```

临时UID使多个并发HTTP请求不会串包。临时UID仅在请求超时前有效，且只有原目标UID可以回复。

HTTP也支持`0-0-0`和`0-0-1`，返回格式与管理员WS一致。

## 超级管理员白名单API

白名单管理只允许通过HTTP `/api`执行，必须同时提供两个Token：

```http
X-Token: <普通共享Token>
X-Super-Token: <超级管理员Token>
```

查询：

```text
7-7-7
```

返回按UID排序、去重后的完整白名单：

```text
7-7-admin*client_1*controller_1
```

全量替换：

```text
8-8-admin*client_2*controller_1
```

成功返回规范化后的新名单：

```text
8-8-admin*client_2*controller_1
```

`8-8-`表示清空白名单。修改会先写入`config.json`，写入成功后再更新内存，并立即断开所有已不在新白名单中的WS连接。配置写入失败时运行时白名单保持不变。

## HTTP状态

| HTTP状态 | 正文 | 含义 |
|---|---|---|
| 200 | 目标回复或清单 | 成功 |
| 400 | `INVALID_MESSAGE` | 三段格式错误 |
| 401 | `UNAUTHORIZED` | Token错误 |
| 403 | `SUPER_ADMIN_REQUIRED` / `UID_NOT_ALLOWED` | 超级Token缺失或UID不在白名单 |
| 404 | `TARGET_NOT_FOUND` | UID从未连接过 |
| 409 | `TARGET_OFFLINE` | UID有历史记录但当前离线 |
| 413 | `MESSAGE_TOO_LARGE` | 超过消息上限 |
| 504 | `TARGET_TIMEOUT` | 目标未在期限内回复 |

## 健康检查

```http
GET /healthz
```

返回：

```text
OK
```
