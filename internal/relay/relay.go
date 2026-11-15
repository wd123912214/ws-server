package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"ws-server/internal/protocol"
)

type Kind string

const (
	KindController Kind = "controller"
	KindClient     Kind = "client"
	KindAdmin      Kind = "admin"
)

var (
	ErrInvalidMessage  = protocol.ErrInvalidMessage
	ErrTargetNotFound  = errors.New("target not found")
	ErrTargetOffline   = errors.New("target offline")
	ErrResponseTimeout = errors.New("response timeout")
	ErrAdminRequired   = errors.New("admin required")
	ErrReplySource     = errors.New("unexpected reply source")
)

type Peer interface {
	UID() string
	Kind() Kind
	Send(message string) error
	CloseDuplicate()
}

type pendingRequest struct {
	expectedUID string
	response    chan string
}

type Relay struct {
	mu      sync.Mutex
	online  map[string]Peer
	history map[string]Kind
	pending map[string]pendingRequest
}

func New() *Relay {
	return &Relay{
		online:  make(map[string]Peer),
		history: make(map[string]Kind),
		pending: make(map[string]pendingRequest),
	}
}

func ValidKind(kind Kind) bool {
	return kind == KindController || kind == KindClient || kind == KindAdmin
}

func (r *Relay) Register(peer Peer) {
	r.mu.Lock()
	old := r.online[peer.UID()]
	r.online[peer.UID()] = peer
	if peer.Kind() != KindAdmin {
		r.history[peer.UID()] = peer.Kind()
	}
	r.mu.Unlock()
	if old != nil && old != peer {
		old.CloseDuplicate()
	}
}

func (r *Relay) Unregister(peer Peer) {
	r.mu.Lock()
	if r.online[peer.UID()] == peer {
		delete(r.online, peer.UID())
	}
	r.mu.Unlock()
}

func (r *Relay) AllClientUIDs() []string {
	r.mu.Lock()
	result := make([]string, 0, len(r.history))
	for uid := range r.history {
		result = append(result, uid)
	}
	r.mu.Unlock()
	sort.Strings(result)
	return result
}

func (r *Relay) OnlineClientUIDs() []string {
	r.mu.Lock()
	result := make([]string, 0, len(r.online))
	for uid, peer := range r.online {
		if peer.Kind() != KindAdmin {
			result = append(result, uid)
		}
	}
	r.mu.Unlock()
	sort.Strings(result)
	return result
}

func (r *Relay) DisconnectNotAllowed(allowed func(string) bool) []string {
	r.mu.Lock()
	peers := make([]Peer, 0)
	uids := make([]string, 0)
	for uid, peer := range r.online {
		if allowed(uid) {
			continue
		}
		delete(r.online, uid)
		peers = append(peers, peer)
		uids = append(uids, uid)
	}
	r.mu.Unlock()
	for _, peer := range peers {
		peer.CloseDuplicate()
	}
	sort.Strings(uids)
	return uids
}

func (r *Relay) Handle(source Peer, raw string) error {
	if raw == "0-0-0" || raw == "0-0-1" {
		if source.Kind() != KindAdmin {
			return ErrAdminRequired
		}
		mode := "0"
		uids := r.AllClientUIDs()
		if raw == "0-0-1" {
			mode = "1"
			uids = r.OnlineClientUIDs()
		}
		return source.Send("0-" + mode + "-" + strings.Join(uids, ","))
	}

	message, err := protocol.Parse(raw)
	if err != nil {
		return err
	}
	if strings.HasPrefix(message.Target, "_http.") {
		return r.resolvePending(source, message)
	}
	return r.forward(source.UID(), message)
}

func (r *Relay) Request(ctx context.Context, raw string, timeout time.Duration) (string, error) {
	message, err := protocol.Parse(raw)
	if err != nil {
		return "", err
	}
	virtualUID, err := newVirtualUID()
	if err != nil {
		return "", err
	}
	pending := pendingRequest{expectedUID: message.Target, response: make(chan string, 1)}
	r.mu.Lock()
	r.pending[virtualUID] = pending
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, virtualUID)
		r.mu.Unlock()
	}()

	if err := r.forward(virtualUID, message); err != nil {
		return "", err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-pending.response:
		return response, nil
	case <-timer.C:
		return "", ErrResponseTimeout
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (r *Relay) forward(sourceUID string, message protocol.Message) error {
	r.mu.Lock()
	target := r.online[message.Target]
	_, seen := r.history[message.Target]
	r.mu.Unlock()
	if target == nil {
		if seen {
			return ErrTargetOffline
		}
		return ErrTargetNotFound
	}
	if err := target.Send(sourceUID + "-" + message.Text1 + "-" + message.Text2); err != nil {
		return ErrTargetOffline
	}
	return nil
}

func (r *Relay) resolvePending(source Peer, message protocol.Message) error {
	r.mu.Lock()
	pending, ok := r.pending[message.Target]
	r.mu.Unlock()
	if !ok {
		return ErrTargetNotFound
	}
	if pending.expectedUID != source.UID() {
		return ErrReplySource
	}
	response := source.UID() + "-" + message.Text1 + "-" + message.Text2
	select {
	case pending.response <- response:
		return nil
	default:
		return ErrResponseTimeout
	}
}

func newVirtualUID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "_http." + hex.EncodeToString(raw[:]), nil
}
