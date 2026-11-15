# 协议

全部消息都是 UTF-8 编码的 JSON 文本。WebSocket 只用文本帧。

## 一、执行端（WebSocket）

连接地址：`ws://域名/ws`。不需要附加任何请求头，身份放在第一条消息里。

### 1. 注册（连接后 10 秒内必须发送）

```json
{"type":"register","uid":"dev_001","token":""}
```

UID 规则：字母、数字、`_`、`.`、`-`，长度 1～64。

成功：

```json
{"type":"registered","uid":"dev_001"}
```

失败（随后服务器会断开连接）：

```json
{"type":"register_fail","msg":"uid_in_use"}
```

| msg | 含义 | 建议 |
|---|---|---|
| `uid_in_use` | 该 UID 已经在线 | 等几秒后重连 |
| `banned` | IP 或 UID 已被拉黑 | 停止重连 |
| `unauthorized` | Token 错误 | 停止重连，检查配置 |
| `bad_uid` / `bad_request` / `register_first` | 格式错误 | 修代码 |

### 2. 心跳

服务器每 30 秒（`heartbeat` 配置）发送：

```json
{"type":"ping","t":1731668400}
```

执行端回复：

```json
{"type":"pong"}
```

超过 2.5 倍心跳间隔没收到执行端**任何**消息，服务器就会断开连接。

服务器不使用 WebSocket 协议层的 Ping/Pong 控制帧，客户端也不需要发控制帧。执行端可以主动发 `{"type":"ping"}`，服务器会回 `{"type":"pong"}`。

### 3. 收任务、回结果

服务器下发：

```json
{"type":"task","id":"a1b2c3d4e5f60718","data":<发送端的data，原样>}
```

执行端回复，`id` 必须和收到的一致：

```json
{"type":"result","id":"a1b2c3d4e5f60718","data":<任意JSON，原样返回给发送端>}
```

`data` 可以是对象、数组、字符串、数字。如果任务已经超时才回复，服务器会返回 `{"type":"error","msg":"task_expired","id":"..."}`，这种情况不算违规。

### 4. 服务器提示

```json
{"type":"error","msg":"..."}
```

`msg` 为 `banned` 或 `kicked` 时，服务器随后会断开连接。其他取值是违规原因，见下方「违规规则」。

## 二、发送端（HTTP）

### 发任务

`POST /api/v1/task`，下面两种正文格式都支持。

**JSON 正文**（推荐，PHP 用这个）：

```json
{"uid":"dev_001","token":"","data":{"cmd":"run"},"timeout":30}
```

**表单正文**（易语言拼起来更方便）：

```
uid=dev_001&token=&data=要发送的文本&timeout=30
```

表单里的 `data` 会作为 JSON 字符串转发，执行端收到的是 `"要发送的文本"`。

- Token 也可以放在请求头 `X-Token` 里。
- `timeout` 可以不填，默认 30 秒，最大 120 秒。
- 请求会一直挂起，直到收到执行端回复或者超时。

**返回固定为 HTTP 200**，只需要看 `code`：

```json
{"code":0,"msg":"ok","data":<执行端回复的data>}
```

| code | msg | 含义 |
|---|---|---|
| 0 | ok | 成功，`data` 是执行端的回复 |
| 1 | offline | 目标 UID 不在线 |
| 2 | timeout | 目标在线，但没有在规定时间内回复 |
| 3 | disconnected | 任务发出去后目标断线了 |
| 4 | bad_request | 参数格式错误 |
| 5 | banned | 发送方 IP 已被拉黑 |
| 6 | unauthorized | Token 错误 |
| 9 | internal_error | 服务器内部错误 |

### 查询在线列表

```
GET /api/v1/clients?token=xxx
```

返回：

```json
{"code":0,"msg":"ok","data":["dev_001","dev_002"]}
```

## 三、违规规则

每次违规会同时给 **IP** 和 **UID** 各记一次（UID 未知时只记 IP）。在 `violation_window`（默认 10 分钟）内累计达到 `violation_limit`（默认 3 次）就拉黑，已在线的连接会被立即踢下线。

| 场景 | 违规原因 |
|---|---|
| 注册消息不是 JSON，或者第一条消息不是 register | 注册消息不是JSON / 未注册先发消息 |
| Token 错误（WS、HTTP 都算） | Token错误 / HTTP Token错误 |
| UID 格式错误 | UID格式错误 |
| 发送二进制帧 | 只接受文本帧 |
| 消息不是合法 JSON | 不是合法JSON |
| 缺少 type、type 未知 | 缺少type / 未知type |
| 已注册后再次发送 register | 重复注册 |
| result 缺少 id | result缺少id |
| result 的 id 属于别的 UID 的任务 | 回复了不属于自己的任务 |
| 消息或 HTTP 正文超过 64KB | 消息超长 |
| 面板口令错误 | 面板口令错误 |

以下情况**不算**违规：目标离线、任务超时后才回复、因 `uid_in_use` 被拒绝。

规则判定都集中在 `server.go`，要增加或修改规则就改那里。

## 四、示例

### PHP

```php
function relay_task($uid, $data, $timeout = 30) {
    $ch = curl_init('http://你的域名/api/v1/task');
    curl_setopt_array($ch, [
        CURLOPT_POST => true,
        CURLOPT_HTTPHEADER => ['Content-Type: application/json', 'X-Token: '],
        CURLOPT_POSTFIELDS => json_encode(['uid' => $uid, 'data' => $data, 'timeout' => $timeout], JSON_UNESCAPED_UNICODE),
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_TIMEOUT => $timeout + 5,
    ]);
    $res = json_decode(curl_exec($ch), true);
    curl_close($ch);
    return $res; // ['code'=>0,'msg'=>'ok','data'=>...]
}
```

注意：curl 的超时要比 `timeout` 多留几秒。

### 易语言：发送端（HTTP）

用你现有的 HTTP 模块，POST 表单或 JSON 都可以：

```
地址：http://你的域名/api/v1/task
请求头：Content-Type: application/x-www-form-urlencoded
正文：uid=dev_001&token=&data=编码后的内容
```

- 正文按 UTF-8 编码；`data` 中如果含有 `&`、`=` 或中文，需要先做 URL 编码。
- 返回的是 UTF-8 JSON，先转成文本，再取 `code` 和 `data`。
- HTTP 模块的超时时间要大于 `timeout`。

### 易语言：执行端（改造旧的 WS 长连接客户端类）

旧的类大部分可以继续用，需要改 4 处：

1. **去掉握手时附加的 `X-UID`、`X-Type`、`X-Token` 请求头**，连接地址不变，仍然是 `/ws`。
2. **连接成功回调**里立刻发送注册消息：`{"type":"register","uid":"你的UID","token":""}`。
3. **收到消息时**先解析 JSON 取 `type`：
   - `ping`：回复 `{"type":"pong"}`，替代旧的原样回复 `0-heartbeat-N`；
   - `task`：取出 `id` 和 `data`，处理完回复 `{"type":"result","id":"同一个id","data":结果}`；
   - `register_fail`：如果 `msg` 是 `banned` 或 `unauthorized`，停止自动重连。
4. **删掉旧的横杠格式**：`目标UID-txt1-txt2` 和 `_http.` 前缀都不再使用。

保持不变的部分：

- 仍然不要发送操作码 9 或 10 的控制帧，服务器也不会发。
- 类对象仍然要声明成程序集变量，不能用局部变量。
- 中文内容按 UTF-8 发送。
