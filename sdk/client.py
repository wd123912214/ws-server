# -*- coding: utf-8 -*-
"""
WS Relay 执行端（Python）
只用标准库，不需要 pip 安装任何东西，Python 3.6+ 可用。

用法：
    from client import RelayClient

    def on_task(data, task_id):
        # 返回值就是给发送方的回复（任何能 json 序列化的东西）
        return {"ok": True, "echo": data}

    RelayClient("ws://你的域名/ws", "dev_001", on_task, token="").run_forever()

任务在独立线程里执行，耗时任务不会耽误心跳。
"""
import base64
import json
import os
import socket
import ssl
import struct
import threading
import time
from urllib.parse import urlparse


class _WS:
    """极简 WebSocket 客户端（只支持文本帧，够用）。"""

    def __init__(self, url, timeout=10):
        u = urlparse(url)
        secure = u.scheme == "wss"
        host, port = u.hostname, u.port or (443 if secure else 80)
        sock = socket.create_connection((host, port), timeout=timeout)
        if secure:
            sock = ssl.create_default_context().wrap_socket(sock, server_hostname=host)
        key = base64.b64encode(os.urandom(16)).decode()
        path = (u.path or "/") + ("?" + u.query if u.query else "")
        host_header = host if u.port is None else "%s:%d" % (host, port)
        req = (
            "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
            "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n" % (path, host_header, key)
        )
        sock.sendall(req.encode())
        resp = b""
        while b"\r\n\r\n" not in resp:
            chunk = sock.recv(1024)
            if not chunk:
                raise ConnectionError("握手失败：连接被关闭")
            resp += chunk
        head, self._buf = resp.split(b"\r\n\r\n", 1)
        status = head.split(b"\r\n", 1)[0]
        if b" 101 " not in status + b" ":
            raise ConnectionError("握手失败：" + status.decode(errors="replace"))
        sock.settimeout(None)
        self.sock = sock
        self._lock = threading.Lock()

    def _read(self, n):
        while len(self._buf) < n:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise ConnectionError("连接已断开")
            self._buf += chunk
        data, self._buf = self._buf[:n], self._buf[n:]
        return data

    def _send_frame(self, opcode, payload):
        header = bytes([0x80 | opcode])
        n = len(payload)
        if n < 126:
            header += bytes([0x80 | n])
        elif n < 65536:
            header += bytes([0x80 | 126]) + struct.pack(">H", n)
        else:
            header += bytes([0x80 | 127]) + struct.pack(">Q", n)
        mask = os.urandom(4)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        with self._lock:
            self.sock.sendall(header + mask + masked)

    def send_text(self, text):
        self._send_frame(0x1, text.encode("utf-8"))

    def recv_text(self):
        """阻塞读取下一条文本消息。连接关闭时抛 ConnectionError。"""
        parts = []
        while True:
            b1, b2 = self._read(2)
            fin, opcode = b1 & 0x80, b1 & 0x0F
            n = b2 & 0x7F
            if n == 126:
                n = struct.unpack(">H", self._read(2))[0]
            elif n == 127:
                n = struct.unpack(">Q", self._read(8))[0]
            mask = self._read(4) if b2 & 0x80 else None
            payload = self._read(n)
            if mask:
                payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
            if opcode == 0x8:
                raise ConnectionError("服务器关闭连接 " + payload[2:].decode("utf-8", "replace"))
            if opcode == 0x9:
                self._send_frame(0xA, payload)
                continue
            if opcode == 0xA:
                continue
            parts.append(payload)
            if fin:
                return b"".join(parts).decode("utf-8")

    def close(self):
        try:
            self._send_frame(0x8, struct.pack(">H", 1000))
        except Exception:
            pass
        try:
            self.sock.close()
        except Exception:
            pass


class RelayClient:
    def __init__(self, url, uid, on_task, token="", reconnect=3, log=print):
        self.url, self.uid, self.on_task, self.token = url, uid, on_task, token
        self.reconnect, self.log = reconnect, log
        self.stopped = False
        self.ws = None

    def send(self, obj):
        self.ws.send_text(json.dumps(obj, ensure_ascii=False))

    def _run_task(self, ws, task_id, data):
        try:
            result = self.on_task(data, task_id)
        except Exception as e:
            result = {"error": str(e)}
        try:
            ws.send_text(json.dumps({"type": "result", "id": task_id, "data": result}, ensure_ascii=False))
        except Exception as e:
            self.log("[relay] 回复失败 %s" % e)

    def run_once(self):
        """连接并处理消息，直到断开。"""
        self.ws = ws = _WS(self.url)
        try:
            self.send({"type": "register", "uid": self.uid, "token": self.token})
            while not self.stopped:
                m = json.loads(ws.recv_text())
                t = m.get("type")
                if t == "registered":
                    self.log("[relay] 已注册 %s" % m.get("uid"))
                elif t == "register_fail":
                    self.log("[relay] 注册失败 %s" % m.get("msg"))
                    if m.get("msg") in ("banned", "unauthorized"):
                        self.stopped = True
                    return
                elif t == "ping":
                    self.send({"type": "pong"})
                elif t == "task":
                    threading.Thread(target=self._run_task, args=(ws, m.get("id"), m.get("data")), daemon=True).start()
                elif t == "error":
                    self.log("[relay] 服务器提示 %s" % m.get("msg"))
                    if m.get("msg") == "banned":
                        self.stopped = True
        finally:
            ws.close()

    def run_forever(self):
        while not self.stopped:
            try:
                self.run_once()
            except Exception as e:
                self.log("[relay] 断开：%s" % e)
            if not self.stopped:
                time.sleep(self.reconnect)
        self.log("[relay] 已停止")

    def stop(self):
        self.stopped = True
        if self.ws:
            self.ws.close()


if __name__ == "__main__":
    import sys

    args = sys.argv[1:] + [None] * 3
    url = args[0] or "ws://127.0.0.1:8080/ws"
    uid = args[1] or "py_demo"
    token = args[2] or ""
    RelayClient(url, uid, lambda data, tid: {"from": uid, "received": data, "at": time.time()}, token=token).run_forever()
