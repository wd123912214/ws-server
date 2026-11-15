// WS Relay 执行端（JavaScript）
// 浏览器、Node.js 22+ 可直接运行（使用内置 WebSocket）；更早的 Node 需 `npm i ws`。
//
// 用法：
//   const { RelayClient } = require('./client.js');
//   new RelayClient('ws://你的域名/ws', 'dev_001', {
//     token: '',
//     onTask: async (data, id) => ({ ok: true, echo: data }),   // 返回值就是给发送方的回复
//   }).start();

class RelayClient {
  constructor(url, uid, { token = '', onTask, reconnectMs = 3000, log = console.log } = {}) {
    this.url = url;
    this.uid = uid;
    this.token = token;
    this.onTask = onTask || (async (data) => data);
    this.reconnectMs = reconnectMs;
    this.log = log;
    this.stopped = false;
    this.ws = null;
  }

  start() {
    this.stopped = false;
    this._connect();
    return this;
  }

  stop() {
    this.stopped = true;
    if (this.ws) this.ws.close();
  }

  _send(obj) {
    if (this.ws && this.ws.readyState === 1) this.ws.send(JSON.stringify(obj));
  }

  _connect() {
    const WS = typeof WebSocket !== 'undefined' ? WebSocket : require('ws');
    const ws = new WS(this.url);
    this.ws = ws;

    ws.onopen = () => this._send({ type: 'register', uid: this.uid, token: this.token });

    ws.onmessage = async (ev) => {
      let m;
      try { m = JSON.parse(typeof ev.data === 'string' ? ev.data : ev.data.toString()); } catch { return; }
      switch (m.type) {
        case 'registered':
          this.log(`[relay] 已注册 ${m.uid}`);
          break;
        case 'register_fail':
          this.log(`[relay] 注册失败 ${m.msg}`);
          if (m.msg === 'banned' || m.msg === 'unauthorized') this.stopped = true; // 不再重连
          break;
        case 'ping':
          this._send({ type: 'pong' });
          break;
        case 'task': {
          let data;
          try { data = await this.onTask(m.data, m.id); }
          catch (e) { data = { error: String(e && e.message || e) }; }
          this._send({ type: 'result', id: m.id, data: data === undefined ? null : data });
          break;
        }
        case 'error':
          this.log(`[relay] 服务器提示 ${m.msg}`);
          if (m.msg === 'banned') this.stopped = true;
          break;
      }
    };

    ws.onclose = () => {
      if (this.ws !== ws) return;
      if (this.stopped) return this.log('[relay] 已停止');
      this.log(`[relay] 断开，${this.reconnectMs / 1000} 秒后重连`);
      setTimeout(() => this._connect(), this.reconnectMs);
    };
    ws.onerror = () => {}; // 交给 onclose 处理
  }
}

if (typeof module !== 'undefined') module.exports = { RelayClient };

// 直接运行：node client.js ws://127.0.0.1:8080/ws dev_001 [token]
if (typeof require !== 'undefined' && typeof module !== 'undefined' && require.main === module) {
  const [url = 'ws://127.0.0.1:8080/ws', uid = 'js_demo', token = ''] = process.argv.slice(2);
  new RelayClient(url, uid, {
    token,
    onTask: async (data) => ({ from: uid, received: data, at: Date.now() }),
  }).start();
}
