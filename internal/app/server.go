package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"ws-server/internal/protocol"
	"ws-server/internal/relay"
	"ws-server/internal/whitelist"
)

type Config struct {
	Token             string
	SuperAdminToken   string
	Whitelist         *whitelist.Manager
	RequestTimeout    time.Duration
	WriteTimeout      time.Duration
	HeartbeatInterval time.Duration
	MaxMessageBytes   int64
	Logger            *slog.Logger
	Debug             bool
}

type Server struct {
	config Config
	relay  *relay.Relay
	mux    *http.ServeMux
}

func New(config Config, r *relay.Relay) *Server {
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 10 * time.Second
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 30 * time.Second
	}
	if config.MaxMessageBytes <= 0 || config.MaxMessageBytes > protocol.MaxMessageBytes {
		config.MaxMessageBytes = protocol.MaxMessageBytes
	}
	if r == nil {
		r = relay.New()
	}
	if config.Whitelist == nil {
		config.Whitelist, _ = whitelist.New(nil, nil)
	}
	s := &Server{config: config, relay: r, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /ws", s.handleWebSocket)
	s.mux.HandleFunc("POST /api", s.handleAPI)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "OK\n")
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.authenticated(r) {
		http.Error(w, "UNAUTHORIZED", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxMessageBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "MESSAGE_TOO_LARGE", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "READ_FAILED", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(string(raw))
	if message == "7-7-7" || strings.HasPrefix(message, "8-8-") {
		s.handleWhitelistAPI(w, r, message)
		return
	}
	if message == "0-0-0" {
		_, _ = io.WriteString(w, "0-0-"+strings.Join(s.relay.AllClientUIDs(), ","))
		return
	}
	if message == "0-0-1" {
		_, _ = io.WriteString(w, "0-1-"+strings.Join(s.relay.OnlineClientUIDs(), ","))
		return
	}
	response, err := s.relay.Request(r.Context(), message, s.config.RequestTimeout)
	if err != nil {
		writeRelayError(w, err)
		return
	}
	_, _ = io.WriteString(w, response)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	remoteAddr := r.RemoteAddr
	if !s.authenticated(r) {
		s.debug("ws_handshake_rejected", "remote", remoteAddr, "reason", "unauthorized")
		http.Error(w, "UNAUTHORIZED", http.StatusUnauthorized)
		return
	}
	uid := r.Header.Get("X-UID")
	kind := relay.Kind(r.Header.Get("X-Type"))
	if !protocol.ValidUID(uid) || !relay.ValidKind(kind) {
		s.debug("ws_handshake_rejected", "remote", remoteAddr, "uid", uid, "type", kind, "reason", "invalid_identity")
		http.Error(w, "INVALID_IDENTITY", http.StatusBadRequest)
		return
	}
	if !s.config.Whitelist.Allowed(uid) {
		s.debug("ws_handshake_rejected", "remote", remoteAddr, "uid", uid, "type", kind, "reason", "uid_not_allowed")
		http.Error(w, "UID_NOT_ALLOWED", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.debug("ws_accept_failed", "remote", remoteAddr, "uid", uid, "type", kind, "error", err)
		return
	}
	s.debug("ws_connected", "remote", remoteAddr, "uid", uid, "type", kind)
	conn.SetReadLimit(s.config.MaxMessageBytes)
	ctx, cancel := context.WithCancel(context.Background())
	peer := &wsPeer{uid: uid, kind: kind, remoteAddr: remoteAddr, conn: conn, ctx: ctx, cancel: cancel, writeTimeout: s.config.WriteTimeout, heartbeatAcks: make(chan string, 8), logger: s.config.Logger, debug: s.config.Debug}
	s.relay.Register(peer)
	defer func() {
		peer.log("ws_cleanup")
		s.relay.Unregister(peer)
		peer.shutdown()
	}()
	go peer.heartbeatLoop(s.config.HeartbeatInterval)

	for {
		messageType, raw, err := conn.Read(ctx)
		if err != nil {
			peer.log("ws_read_error", "close_status", websocket.CloseStatus(err), "error", err)
			return
		}
		peer.log("ws_frame_read", "message_type", messageType, "length", len(raw), "preview", messagePreview(raw))
		if messageType != websocket.MessageText {
			if peer.Send("0-error-TEXT_ONLY") != nil {
				return
			}
			continue
		}
		message := strings.TrimSpace(string(raw))
		if peer.acceptHeartbeatAck(message) {
			continue
		}
		if err := s.relay.Handle(peer, message); err != nil {
			if peer.Send("0-error-"+relayErrorCode(err)) != nil {
				return
			}
		}
	}
}

func (s *Server) debug(message string, args ...any) {
	if s.config.Debug && s.config.Logger != nil {
		s.config.Logger.Info("[WS-DEBUG] "+message, args...)
	}
}

func messagePreview(raw []byte) string {
	const limit = 256
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "..."
}

func (s *Server) handleWhitelistAPI(w http.ResponseWriter, r *http.Request, message string) {
	if !constantTokenMatch(r.Header.Get("X-Super-Token"), s.config.SuperAdminToken) {
		http.Error(w, "SUPER_ADMIN_REQUIRED", http.StatusForbidden)
		return
	}
	if message == "7-7-7" {
		_, _ = io.WriteString(w, "7-7-"+strings.Join(s.config.Whitelist.List(), "*"))
		return
	}
	rawList := strings.TrimPrefix(message, "8-8-")
	var uids []string
	if rawList != "" {
		uids = strings.Split(rawList, "*")
	}
	updated, err := s.config.Whitelist.Replace(uids)
	if errors.Is(err, whitelist.ErrInvalidUID) {
		http.Error(w, "INVALID_WHITELIST", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "WHITELIST_SAVE_FAILED", http.StatusInternalServerError)
		return
	}
	s.relay.DisconnectNotAllowed(s.config.Whitelist.Allowed)
	_, _ = io.WriteString(w, "8-8-"+strings.Join(updated, "*"))
}

func (s *Server) authenticated(r *http.Request) bool {
	return constantTokenMatch(r.Header.Get("X-Token"), s.config.Token)
}

func constantTokenMatch(provided, expected string) bool {
	if provided == "" || expected == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func writeRelayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, relay.ErrInvalidMessage):
		http.Error(w, "INVALID_MESSAGE", http.StatusBadRequest)
	case errors.Is(err, relay.ErrTargetNotFound):
		http.Error(w, "TARGET_NOT_FOUND", http.StatusNotFound)
	case errors.Is(err, relay.ErrTargetOffline):
		http.Error(w, "TARGET_OFFLINE", http.StatusConflict)
	case errors.Is(err, relay.ErrResponseTimeout), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "TARGET_TIMEOUT", http.StatusGatewayTimeout)
	case errors.Is(err, context.Canceled):
		http.Error(w, "REQUEST_CANCELED", 499)
	default:
		http.Error(w, "INTERNAL_ERROR", http.StatusInternalServerError)
	}
}

func relayErrorCode(err error) string {
	switch {
	case errors.Is(err, relay.ErrInvalidMessage):
		return "INVALID_MESSAGE"
	case errors.Is(err, relay.ErrTargetNotFound):
		return "TARGET_NOT_FOUND"
	case errors.Is(err, relay.ErrTargetOffline):
		return "TARGET_OFFLINE"
	case errors.Is(err, relay.ErrAdminRequired):
		return "ADMIN_REQUIRED"
	case errors.Is(err, relay.ErrReplySource):
		return "INVALID_REPLY_SOURCE"
	default:
		return "INTERNAL_ERROR"
	}
}

type wsPeer struct {
	uid           string
	kind          relay.Kind
	remoteAddr    string
	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	writeTimeout  time.Duration
	writeMu       sync.Mutex
	closeOnce     sync.Once
	heartbeatAcks chan string
	logger        *slog.Logger
	debug         bool
}

func (p *wsPeer) UID() string      { return p.uid }
func (p *wsPeer) Kind() relay.Kind { return p.kind }

func (p *wsPeer) Send(message string) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.log("ws_text_send", "length", len(message), "preview", messagePreview([]byte(message)))
	ctx, cancel := context.WithTimeout(p.ctx, p.writeTimeout)
	defer cancel()
	err := p.conn.Write(ctx, websocket.MessageText, []byte(message))
	if err != nil {
		p.log("ws_text_send_error", "error", err)
	}
	return err
}

func (p *wsPeer) CloseDuplicate() {
	p.log("ws_server_forced_close", "reason", "duplicate_uid_or_whitelist_removed")
	p.shutdown()
}

func (p *wsPeer) shutdown() {
	p.closeOnce.Do(func() {
		p.log("ws_shutdown")
		p.cancel()
		_ = p.conn.CloseNow()
	})
}

func (p *wsPeer) heartbeatLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var sequence int64
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			sequence++
			started := time.Now()
			expected := strconv.FormatInt(sequence, 10)
			message := "0-heartbeat-" + expected
			p.log("heartbeat_text_send", "sequence", sequence, "message", message)
			if err := p.Send(message); err != nil {
				p.log("heartbeat_text_send_failed", "sequence", sequence, "elapsed", time.Since(started), "error", err)
				p.shutdown()
				return
			}
			if !p.waitHeartbeatAck(expected) {
				p.log("heartbeat_text_timeout", "sequence", sequence, "elapsed", time.Since(started))
				p.shutdown()
				return
			}
			p.log("heartbeat_text_ok", "sequence", sequence, "elapsed", time.Since(started))
		}
	}
}

func (p *wsPeer) acceptHeartbeatAck(message string) bool {
	const prefix = "0-heartbeat-"
	if !strings.HasPrefix(message, prefix) {
		return false
	}
	ack := strings.TrimPrefix(message, prefix)
	p.log("heartbeat_text_received", "ack", ack)
	select {
	case p.heartbeatAcks <- ack:
	default:
		p.log("heartbeat_text_ack_dropped", "ack", ack)
	}
	return true
}

func (p *wsPeer) waitHeartbeatAck(expected string) bool {
	timer := time.NewTimer(p.writeTimeout)
	defer timer.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return false
		case <-timer.C:
			return false
		case ack := <-p.heartbeatAcks:
			if ack == expected {
				return true
			}
			p.log("heartbeat_text_stale_ack", "expected", expected, "actual", ack)
		}
	}
}

func (p *wsPeer) log(message string, args ...any) {
	if !p.debug || p.logger == nil {
		return
	}
	base := []any{"uid", p.uid, "type", p.kind, "remote", p.remoteAddr}
	base = append(base, args...)
	p.logger.Info("[WS-DEBUG] "+message, base...)
}

var _ relay.Peer = (*wsPeer)(nil)
