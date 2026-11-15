package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var uidPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

type Server struct {
	cfg     Config
	hub     *Hub
	guard   *Guard
	started time.Time
	mux     *http.ServeMux
}

func newServer(cfg Config, guard *Guard) *Server {
	s := &Server{cfg: cfg, hub: newHub(cfg.heartbeat()), guard: guard, started: time.Now(), mux: http.NewServeMux()}
	guard.onBan = s.hub.kickBy
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok "+version) })
	s.mux.HandleFunc("GET /ws", s.handleWS)
	s.mux.HandleFunc("POST /api/v1/task", s.handleTask)
	s.mux.HandleFunc("GET /api/v1/clients", s.handleClients)
	s.registerPanel()
	return s
}

// clientIP 取真实IP。trust_proxy 开启时信任 nginx 传来的头。
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tokenOK(got, want string) bool {
	if want == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// violate 记违规：IP 和 UID 各计一次。返回是否任一被拉黑。
func (s *Server) violate(ip, uid, reason string) bool {
	logger.Warn("违规", "ip", ip, "uid", uid, "reason", reason)
	b1 := s.guard.Violate("ip", ip, reason)
	b2 := uid != "" && s.guard.Violate("uid", uid, reason)
	return b1 || b2
}

// ---------------- WebSocket（执行端） ----------------

type inMessage struct {
	Type  string          `json:"type"`
	UID   string          `json:"uid"`
	Token string          `json:"token"`
	ID    string          `json:"id"`
	Data  json.RawMessage `json:"data"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.guard.Banned("ip", ip) {
		http.Error(w, "banned", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // 允许任意 Origin（浏览器 JS 执行端）
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(s.cfg.MaxMessageBytes)
	var regMu sync.Mutex

	reject := func(msg string) {
		_ = writeJSON(conn, &regMu, map[string]string{"type": "register_fail", "msg": msg})
		conn.Close(websocket.StatusPolicyViolation, msg)
	}

	// 1. 注册：连接后 10 秒内必须发 register
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	typ, raw, err := conn.Read(ctx)
	cancel()
	if err != nil {
		if isTooBig(err) {
			s.violate(ip, "", "消息超长")
		}
		conn.CloseNow()
		return
	}
	var m inMessage
	switch {
	case typ != websocket.MessageText || json.Unmarshal(raw, &m) != nil:
		s.violate(ip, "", "注册消息不是JSON")
		reject("bad_request")
		return
	case m.Type != "register":
		s.violate(ip, "", "未注册先发消息")
		reject("register_first")
		return
	case !tokenOK(m.Token, s.cfg.Token):
		s.violate(ip, "", "Token错误")
		reject("unauthorized")
		return
	case !uidPattern.MatchString(m.UID):
		s.violate(ip, "", "UID格式错误")
		reject("bad_uid")
		return
	case s.guard.Banned("uid", m.UID):
		reject("banned")
		return
	}

	c := newClient(m.UID, ip, conn)
	if !s.hub.add(c) {
		reject("uid_in_use")
		return
	}
	defer s.hub.remove(c)
	if err := c.send(map[string]string{"type": "registered", "uid": c.UID}); err != nil {
		return
	}
	logger.Info("上线", "uid", c.UID, "ip", ip)
	defer logger.Info("下线", "uid", c.UID, "ip", ip)

	// 2. 心跳：服务器定时发 {"type":"ping"}，客户端回 {"type":"pong"}。
	// 不用 WS 协议层 Ping 帧，因为易语言 HWA 模块回的 Pong 不符合规范。
	loopCtx, stop := context.WithCancel(context.Background())
	defer stop()
	go s.heartbeat(loopCtx, c)

	// 3. 消息循环
	for {
		typ, raw, err := conn.Read(loopCtx)
		if err != nil {
			if isTooBig(err) {
				s.violate(ip, c.UID, "消息超长")
			}
			return
		}
		c.touch()
		reason := s.handleClientMessage(c, typ, raw)
		if reason == "" {
			continue
		}
		_ = c.send(map[string]string{"type": "error", "msg": reason})
		if s.violate(ip, c.UID, reason) {
			return
		}
	}
}

// handleClientMessage 处理一条执行端消息，返回违规原因（空=正常）。
func (s *Server) handleClientMessage(c *Client, typ websocket.MessageType, raw []byte) string {
	if typ != websocket.MessageText {
		return "只接受文本帧"
	}
	var m inMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "不是合法JSON"
	}
	switch m.Type {
	case "pong":
	case "ping":
		_ = c.send(map[string]string{"type": "pong"})
	case "result":
		if m.ID == "" {
			return "result缺少id"
		}
		ok, foreign := s.hub.resolve(c, m.ID, m.Data)
		if foreign {
			return "回复了不属于自己的任务"
		}
		if !ok {
			// 已超时的任务回来晚了，正常情况，不算违规
			_ = c.send(map[string]string{"type": "error", "msg": "task_expired", "id": m.ID})
		}
	case "register":
		return "重复注册"
	case "":
		return "缺少type"
	default:
		return "未知type: " + truncate(m.Type, 32)
	}
	return ""
}

func (s *Server) heartbeat(ctx context.Context, c *Client) {
	interval := s.cfg.heartbeat()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c.idle() > interval*2+interval/2 {
				logger.Info("心跳超时", "uid", c.UID)
				c.conn.CloseNow()
				return
			}
			if c.send(map[string]any{"type": "ping", "t": time.Now().Unix()}) != nil {
				c.conn.CloseNow()
				return
			}
		}
	}
}

func isTooBig(err error) bool {
	return websocket.CloseStatus(err) == websocket.StatusMessageTooBig || strings.Contains(err.Error(), "read limited")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------------- HTTP（发送端） ----------------

type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data,omitempty"`
}

// writeAPI 统一返回 HTTP 200 + JSON，调用方只看 code。
func writeAPI(w http.ResponseWriter, code int, data json.RawMessage) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(apiResponse{Code: code, Msg: codeMsg[code], Data: data})
}

type taskRequest struct {
	UID     string
	Token   string
	Data    json.RawMessage
	Timeout int
}

// parseTask 同时支持 JSON 正文和表单（易语言拼表单更方便）。
// 表单里的 data 按字符串转发。
func parseTask(r *http.Request, body []byte) (taskRequest, error) {
	var req taskRequest
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var j struct {
			UID     string          `json:"uid"`
			Token   string          `json:"token"`
			Data    json.RawMessage `json:"data"`
			Timeout int             `json:"timeout"`
		}
		if err := json.Unmarshal(trimmed, &j); err != nil {
			return req, err
		}
		req = taskRequest{UID: j.UID, Token: j.Token, Data: j.Data, Timeout: j.Timeout}
	} else {
		form, err := url.ParseQuery(string(trimmed))
		if err != nil {
			return req, err
		}
		if !form.Has("data") {
			return req, errors.New("缺少data")
		}
		data, _ := json.Marshal(form.Get("data"))
		req = taskRequest{UID: form.Get("uid"), Token: form.Get("token"), Data: data}
		req.Timeout, _ = strconv.Atoi(form.Get("timeout"))
	}
	if req.Token == "" {
		req.Token = r.Header.Get("X-Token")
	}
	if len(req.Data) == 0 {
		return req, errors.New("缺少data")
	}
	return req, nil
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.guard.Banned("ip", ip) {
		writeAPI(w, CodeBanned, nil)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxMessageBytes))
	if err != nil {
		s.violate(ip, "", "HTTP正文超长")
		writeAPI(w, CodeBadRequest, nil)
		return
	}
	req, err := parseTask(r, body)
	if err != nil {
		s.violate(ip, "", "HTTP格式错误")
		writeAPI(w, CodeBadRequest, nil)
		return
	}
	if !tokenOK(req.Token, s.cfg.Token) {
		s.violate(ip, "", "HTTP Token错误")
		writeAPI(w, CodeUnauthorized, nil)
		return
	}
	if !uidPattern.MatchString(req.UID) {
		s.violate(ip, "", "HTTP UID格式错误")
		writeAPI(w, CodeBadRequest, nil)
		return
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = s.cfg.TaskTimeout
	}
	if timeout > s.cfg.MaxTaskTimeout {
		timeout = s.cfg.MaxTaskTimeout
	}
	res := s.hub.dispatch(r.Context(), req.UID, req.Data, time.Duration(timeout)*time.Second)
	writeAPI(w, res.code, res.data)
}

// handleClients 发送端查询在线UID列表。
func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.guard.Banned("ip", ip) {
		writeAPI(w, CodeBanned, nil)
		return
	}
	token := r.Header.Get("X-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if !tokenOK(token, s.cfg.Token) {
		s.violate(ip, "", "HTTP Token错误")
		writeAPI(w, CodeUnauthorized, nil)
		return
	}
	data, _ := json.Marshal(s.hub.uids())
	writeAPI(w, CodeOK, data)
}
