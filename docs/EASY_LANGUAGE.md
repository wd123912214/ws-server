# 易语言接入要点

## WebSocket

使用你现有的易语言WebSocket模块连接：

```text
ws://服务器IP:8080/ws
```

握手必须附加：

```text
X-UID: client_001
X-Type: client
X-Token: config.json中的Token
```

本地中控将`X-Type`改为`controller`，管理员改为`admin`。

连接成功后只收发文本帧。发送：

```text
目标UID-txt1-txt2
```

收到：

```text
来源UID-txt1-txt2
```

如果来源UID以`_http.`开头，说明消息来自HTTP一次性API。处理完成后必须把该临时UID作为目标回复：

```text
_http.临时ID-result-ok
```

服务器每30秒发送普通文本心跳`0-heartbeat-序号`。易语言收到后必须使用文本帧原样回复；不要发送WebSocket Ping/Pong控制帧。断线后建议等待3至5秒重连。

UID还必须已经加入`config.json`的`uid_whitelist`。如果未加入，WS握手会返回HTTP 403。

## NxHTTP一次性调用

下面沿用旧项目`易语言http`资料里的NxHTTP接口：

```text
.子程序 调用WS服务器HTTP, 文本型
.参数 服务器地址, 文本型
.参数 Token, 文本型
.参数 三段内容, 文本型
.局部变量 会话, NxHTTP会话
.局部变量 响应ID, 整数型
.局部变量 结果, 文本型

.如果 (会话.创建 () ＝ 假)
    返回 (“HTTP会话创建失败: ” ＋ NxHTTP_取最后错误 ())
.如果结束
会话.置编码 (“UTF-8”)
会话.置请求头 (“X-Token”, Token)
响应ID ＝ 会话.POST (服务器地址 ＋ “/api”, 三段内容, “text/plain”)
.如果 (响应ID ≠ 0)
    结果 ＝ “HTTP ” ＋ 到文本 (NX_GetStatusCode (响应ID)) ＋ “ ” ＋ 指针到文本 (NX_GetBody (响应ID))
    NX_ReleaseResponse (响应ID)
.否则
    结果 ＝ “请求失败: ” ＋ NxHTTP_取最后错误 ()
.如果结束
会话.销毁 ()
返回 (结果)
```

调用示例：

```text
结果 ＝ 调用WS服务器HTTP (“http://服务器IP:8080”, Token, “client_001-command-data”)
```

管理员查询：

```text
所有记录 ＝ 调用WS服务器HTTP (服务器地址, Token, “0-0-0”)
在线记录 ＝ 调用WS服务器HTTP (服务器地址, Token, “0-0-1”)
```

## 超级管理员白名单

超级管理员请求在普通`X-Token`之外，再增加：

```text
会话.置请求头 (“X-Super-Token”, 超级管理员Token)
```

请求正文：

```text
7-7-7
8-8-admin*client_001*controller_001
```

第二条是全量替换而不是追加；不在新名单中的在线连接会被服务器断开。
