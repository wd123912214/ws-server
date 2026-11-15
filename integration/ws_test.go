//go:build integration

package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ws-server/internal/app"
	"ws-server/internal/relay"
	"ws-server/internal/whitelist"
)

func TestWebSocketAndHTTPRoundTrip(t *testing.T) {
	const token = "integration-secret"
	const superToken = "integration-super-secret"
	allowed, err := whitelist.New([]string{"source", "target"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.New(app.Config{Token: token, SuperAdminToken: superToken, Whitelist: allowed, RequestTimeout: time.Second}, relay.New()).Handler())
	defer server.Close()

	source := dial(t, server.URL, token, "source", "controller")
	defer source.CloseNow()
	target := dial(t, server.URL, token, "target", "client")
	defer target.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := source.Write(ctx, websocket.MessageText, []byte("target-command-payload-more")); err != nil {
		t.Fatal(err)
	}
	_, forwarded, err := target.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwarded) != "source-command-payload-more" {
		t.Fatalf("forwarded = %q", forwarded)
	}

	responseCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api", strings.NewReader("target-query-value"))
		request.Header.Set("X-Token", token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			errCh <- err
			return
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			errCh <- err
			return
		}
		responseCh <- string(raw)
	}()

	_, httpForwarded, err := target.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(httpForwarded), "-", 3)
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "_http.") || parts[1] != "query" || parts[2] != "value" {
		t.Fatalf("HTTP forwarding = %q", httpForwarded)
	}
	if err := target.Write(ctx, websocket.MessageText, []byte(parts[0]+"-answer-ok")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		t.Fatal(err)
	case response := <-responseCh:
		if response != "target-answer-ok" {
			t.Fatalf("HTTP response = %q", response)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	query := post(t, ctx, server.URL+"/api", token, superToken, "7-7-7")
	if query != "7-7-source*target" {
		t.Fatalf("whitelist query = %q", query)
	}
	replaced := post(t, ctx, server.URL+"/api", token, superToken, "8-8-target")
	if replaced != "8-8-target" {
		t.Fatalf("whitelist replace = %q", replaced)
	}
}

func TestApplicationHeartbeatRoundTrip(t *testing.T) {
	const token = "heartbeat-secret"
	allowed, err := whitelist.New([]string{"client"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.New(app.Config{
		Token:             token,
		Whitelist:         allowed,
		HeartbeatInterval: 20 * time.Millisecond,
		WriteTimeout:      time.Second,
		MaxMessageBytes:   1024,
	}, relay.New()).Handler())
	defer server.Close()

	client := dial(t, server.URL, token, "client", "client")
	defer client.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	messageType, heartbeat, err := client.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText || string(heartbeat) != "0-heartbeat-1" {
		t.Fatalf("heartbeat = type %v, body %q", messageType, heartbeat)
	}
	if err := client.Write(ctx, websocket.MessageText, heartbeat); err != nil {
		t.Fatal(err)
	}

	messageType, heartbeat, err = client.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText || string(heartbeat) != "0-heartbeat-2" {
		t.Fatalf("second heartbeat = type %v, body %q", messageType, heartbeat)
	}
}

func dial(t *testing.T, serverURL, token, uid, kind string) *websocket.Conn {
	t.Helper()
	header := make(http.Header)
	header.Set("X-Token", token)
	header.Set("X-UID", uid)
	header.Set("X-Type", kind)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(serverURL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if response != nil {
			t.Fatalf("dial %s: %v (HTTP %d)", uid, err, response.StatusCode)
		}
		t.Fatalf("dial %s: %v", uid, err)
	}
	return conn
}

func post(t *testing.T, ctx context.Context, url, token, superToken, body string) string {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Token", token)
	request.Header.Set("X-Super-Token", superToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST %q: HTTP %d %s", body, response.StatusCode, raw)
	}
	return string(raw)
}
