package main

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// 返回给 HTTP 调用方的 code
const (
	CodeOK           = 0
	CodeOffline      = 1
	CodeTimeout      = 2
	CodeDisconnected = 3
	CodeBadRequest   = 4
	CodeBanned       = 5
	CodeUnauthorized = 6
	CodeInternal     = 9
)

var codeMsg = map[int]string{
	CodeOK:           "ok",
	CodeOffline:      "offline",
	CodeTimeout:      "timeout",
	CodeDisconnected: "disconnected",
	CodeBadRequest:   "bad_request",
	CodeBanned:       "banned",
	CodeUnauthorized: "unauthorized",
	CodeInternal:     "internal_error",
}

// Client 一个已注册的执行端连接。
type Client struct {
	UID         string
	IP          string
	ConnectedAt time.Time

	conn      *websocket.Conn
	lastSeen  atomic.Int64
	sent      atomic.Int64
	done      atomic.Int64
	writeMu   sync.Mutex
	closeOnce sync.Once
}

func newClient(uid, ip string, conn *websocket.Conn) *Client {
	c := &Client{UID: uid, IP: ip, ConnectedAt: time.Now(), conn: conn}
	c.touch()
	return c
}

func (c *Client) touch()              { c.lastSeen.Store(time.Now().UnixMilli()) }
func (c *Client) idle() time.Duration { return time.Since(time.UnixMilli(c.lastSeen.Load())) }
func (c *Client) send(v any) error    { return writeJSON(c.conn, &c.writeMu, v) }
func (c *Client) close(reason string) {
	c.closeOnce.Do(func() {
		go c.conn.Close(websocket.StatusPolicyViolation, reason)
	})
}

func writeJSON(conn *websocket.Conn, mu *sync.Mutex, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, raw)
}

type taskResult struct {
	code int
	data json.RawMessage
}

type pendingTask struct {
	uid    string
	client *Client // 任务发给的具体连接，用于断线时只失败这条连接上的任务
	ch     chan taskResult
}

// Stats 累计统计，面板展示用。
type Stats struct {
	Total        atomic.Int64
	OK           atomic.Int64
	Offline      atomic.Int64
	Timeout      atomic.Int64
	Disconnected atomic.Int64
}

// Hub 维护 UID→连接 和 任务ID→等待者。
type Hub struct {
	mu         sync.Mutex
	clients    map[string]*Client
	pending    map[string]*pendingTask
	staleAfter time.Duration
	stats      Stats
}

func newHub(heartbeat time.Duration) *Hub {
	return &Hub{
		clients:    map[string]*Client{},
		pending:    map[string]*pendingTask{},
		staleAfter: heartbeat + 10*time.Second,
	}
}

// add 注册连接。同UID已在线则拒绝新连接；
// 但如果旧连接已经超过一个心跳周期没有任何消息（掉线未被发现的僵尸连接），则踢掉旧的接受新的，
// 避免客户端断网重连时被自己的旧连接挡住。
func (h *Hub) add(c *Client) bool {
	h.mu.Lock()
	old := h.clients[c.UID]
	if old != nil && old.idle() < h.staleAfter {
		h.mu.Unlock()
		return false
	}
	h.clients[c.UID] = c
	h.mu.Unlock()
	if old != nil {
		h.failPending(old)
		old.close("replaced_stale")
	}
	return true
}

// remove 注销连接，并让该连接上所有未完成的任务立即返回 disconnected。
func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	if h.clients[c.UID] == c {
		delete(h.clients, c.UID)
	}
	h.mu.Unlock()
	h.failPending(c)
}

func (h *Hub) failPending(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, p := range h.pending {
		if p.client == c {
			select {
			case p.ch <- taskResult{code: CodeDisconnected}:
			default:
			}
			delete(h.pending, id)
		}
	}
}

func (h *Hub) get(uid string) *Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[uid]
}

// kickBy 按 ip 或 uid 踢下线（拉黑时调用）。
func (h *Hub) kickBy(kind, value string) {
	h.mu.Lock()
	var list []*Client
	for _, c := range h.clients {
		if (kind == "uid" && c.UID == value) || (kind == "ip" && c.IP == value) {
			list = append(list, c)
		}
	}
	h.mu.Unlock()
	for _, c := range list {
		_ = c.send(map[string]string{"type": "error", "msg": "banned"})
		c.close("banned")
	}
}

// dispatch 把任务发给 uid 并等待回复。
func (h *Hub) dispatch(ctx context.Context, uid string, data json.RawMessage, timeout time.Duration) taskResult {
	h.stats.Total.Add(1)
	c := h.get(uid)
	if c == nil {
		h.stats.Offline.Add(1)
		return taskResult{code: CodeOffline}
	}
	id := randHex(8)
	p := &pendingTask{uid: uid, client: c, ch: make(chan taskResult, 1)}
	h.mu.Lock()
	h.pending[id] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}()

	if err := c.send(map[string]any{"type": "task", "id": id, "data": data}); err != nil {
		h.stats.Disconnected.Add(1)
		return taskResult{code: CodeDisconnected}
	}
	c.sent.Add(1)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-p.ch:
		switch r.code {
		case CodeOK:
			h.stats.OK.Add(1)
		case CodeDisconnected:
			h.stats.Disconnected.Add(1)
		}
		return r
	case <-timer.C:
		h.stats.Timeout.Add(1)
		return taskResult{code: CodeTimeout}
	case <-ctx.Done():
		return taskResult{code: CodeInternal}
	}
}

// resolve 执行端回复结果。返回 ok=找到任务；foreign=任务ID属于别的UID（算违规）。
func (h *Hub) resolve(c *Client, id string, data json.RawMessage) (ok, foreign bool) {
	h.mu.Lock()
	p := h.pending[id]
	if p == nil {
		h.mu.Unlock()
		return false, false
	}
	if p.uid != c.UID {
		h.mu.Unlock()
		return false, true
	}
	if p.client != c {
		h.mu.Unlock()
		return false, false
	}
	delete(h.pending, id)
	h.mu.Unlock()
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	p.ch <- taskResult{code: CodeOK, data: data}
	c.done.Add(1)
	return true, false
}

// ClientInfo 面板显示用。
type ClientInfo struct {
	UID         string `json:"uid"`
	IP          string `json:"ip"`
	ConnectedAt int64  `json:"connected_at"`
	LastSeen    int64  `json:"last_seen"`
	Sent        int64  `json:"sent"`
	Done        int64  `json:"done"`
	Pending     int    `json:"pending"`
}

func (h *Hub) list() []ClientInfo {
	h.mu.Lock()
	pending := map[string]int{}
	for _, p := range h.pending {
		pending[p.uid]++
	}
	out := make([]ClientInfo, 0, len(h.clients))
	for _, c := range h.clients {
		out = append(out, ClientInfo{
			UID: c.UID, IP: c.IP,
			ConnectedAt: c.ConnectedAt.Unix(),
			LastSeen:    c.lastSeen.Load() / 1000,
			Sent:        c.sent.Load(), Done: c.done.Load(),
			Pending: pending[c.UID],
		})
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}

func (h *Hub) uids() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.clients))
	for uid := range h.clients {
		out = append(out, uid)
	}
	sort.Strings(out)
	return out
}
