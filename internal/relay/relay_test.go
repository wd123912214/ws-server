package relay

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakePeer struct {
	mu       sync.Mutex
	uid      string
	kind     Kind
	messages []string
	closed   bool
}

func (p *fakePeer) UID() string { return p.uid }
func (p *fakePeer) Kind() Kind  { return p.kind }
func (p *fakePeer) Send(message string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, message)
	return nil
}
func (p *fakePeer) CloseDuplicate() { p.closed = true }
func (p *fakePeer) Messages() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.messages...)
}

func TestRegisterDeduplicatesAndTracksClientHistory(t *testing.T) {
	r := New()
	old := &fakePeer{uid: "client-1", kind: KindClient}
	newer := &fakePeer{uid: "client-1", kind: KindClient}
	admin := &fakePeer{uid: "boss", kind: KindAdmin}

	r.Register(old)
	r.Register(admin)
	r.Register(newer)

	if !old.closed {
		t.Fatal("old duplicate connection was not closed")
	}
	if got := r.OnlineClientUIDs(); !slices.Equal(got, []string{"client-1"}) {
		t.Fatalf("online = %v", got)
	}
	r.Unregister(newer)
	if got := r.AllClientUIDs(); !slices.Equal(got, []string{"client-1"}) {
		t.Fatalf("history = %v", got)
	}
	if len(r.OnlineClientUIDs()) != 0 {
		t.Fatal("unregistered client is still online")
	}
}

func TestRouteRewritesTargetToSourceAndDistinguishesMissingOffline(t *testing.T) {
	r := New()
	source := &fakePeer{uid: "source", kind: KindController}
	target := &fakePeer{uid: "target", kind: KindClient}
	r.Register(source)
	r.Register(target)

	if err := r.Handle(source, "target-txt1-txt2-more"); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if messages := target.Messages(); !slices.Equal(messages, []string{"source-txt1-txt2-more"}) {
		t.Fatalf("target messages = %v", messages)
	}

	r.Unregister(target)
	if err := r.Handle(source, "target-a-b"); !errors.Is(err, ErrTargetOffline) {
		t.Fatalf("offline error = %v", err)
	}
	if err := r.Handle(source, "never-seen-a-b"); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestAdminListCommands(t *testing.T) {
	r := New()
	admin := &fakePeer{uid: "boss", kind: KindAdmin}
	clientB := &fakePeer{uid: "b", kind: KindClient}
	clientA := &fakePeer{uid: "a", kind: KindController}
	r.Register(admin)
	r.Register(clientB)
	r.Register(clientA)
	r.Unregister(clientB)

	if err := r.Handle(admin, "0-0-0"); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(admin, "0-0-1"); err != nil {
		t.Fatal(err)
	}
	if messages := admin.Messages(); !slices.Equal(messages, []string{"0-0-a,b", "0-1-a"}) {
		t.Fatalf("admin messages = %v", messages)
	}
	if err := r.Handle(clientA, "0-0-0"); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("non-admin list error = %v", err)
	}
}

func TestHTTPRequestRoundTripUsesVirtualSourceAndDoesNotCrossTalk(t *testing.T) {
	r := New()
	target := &fakePeer{uid: "target", kind: KindClient}
	r.Register(target)

	type result struct {
		value string
		err   error
	}
	results := make(chan result, 2)
	for _, request := range []string{"target-one-A", "target-two-B"} {
		request := request
		go func() {
			value, err := r.Request(context.Background(), request, time.Second)
			results <- result{value, err}
		}()
	}

	deadline := time.Now().Add(time.Second)
	for len(target.Messages()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	forwardedMessages := target.Messages()
	if len(forwardedMessages) != 2 {
		t.Fatalf("forwarded messages = %v", forwardedMessages)
	}
	for _, forwarded := range forwardedMessages {
		parts := split3(t, forwarded)
		reply := parts[0] + "-reply-" + parts[1]
		if err := r.Handle(target, reply); err != nil {
			t.Fatalf("reply Handle: %v", err)
		}
	}

	got := []string{(<-results).value, (<-results).value}
	slices.Sort(got)
	if !slices.Equal(got, []string{"target-reply-one", "target-reply-two"}) {
		t.Fatalf("results = %v", got)
	}
}

func TestHTTPRequestTimeoutAndTargetErrors(t *testing.T) {
	r := New()
	if _, err := r.Request(context.Background(), "missing-a-b", time.Millisecond); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	target := &fakePeer{uid: "target", kind: KindClient}
	r.Register(target)
	r.Unregister(target)
	if _, err := r.Request(context.Background(), "target-a-b", time.Millisecond); !errors.Is(err, ErrTargetOffline) {
		t.Fatalf("offline error = %v", err)
	}
	r.Register(target)
	if _, err := r.Request(context.Background(), "target-a-b", time.Millisecond); !errors.Is(err, ErrResponseTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestDisconnectNotAllowedClosesAndRemovesPeers(t *testing.T) {
	r := New()
	kept := &fakePeer{uid: "kept", kind: KindClient}
	removed := &fakePeer{uid: "removed", kind: KindController}
	r.Register(kept)
	r.Register(removed)
	disconnected := r.DisconnectNotAllowed(func(uid string) bool { return uid == "kept" })
	if !slices.Equal(disconnected, []string{"removed"}) || !removed.closed || kept.closed {
		t.Fatalf("disconnected=%v kept.closed=%v removed.closed=%v", disconnected, kept.closed, removed.closed)
	}
	if got := r.OnlineClientUIDs(); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("online=%v", got)
	}
}

func split3(t *testing.T, raw string) [3]string {
	t.Helper()
	for i := 0; i < len(raw); i++ {
		if raw[i] != '-' {
			continue
		}
		for j := i + 1; j < len(raw); j++ {
			if raw[j] == '-' {
				return [3]string{raw[:i], raw[i+1 : j], raw[j+1:]}
			}
		}
	}
	t.Fatalf("invalid message %q", raw)
	return [3]string{}
}
