package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testServer(t *testing.T, mut func(*Config)) (*Server, *httptest.Server) {
	t.Helper()
	cfg := defaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.TrustProxy = false
	cfg.Token = "tk"
	cfg.PanelPassword = "pw"
	cfg.TaskTimeout = 2
	if mut != nil {
		mut(&cfg)
	}
	g, err := newGuard(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(cfg, g)
	ts := httptest.NewServer(s.mux)
	t.Cleanup(ts.Close)
	return s, ts
}

type wsc struct {
	t    *testing.T
	conn *websocket.Conn
}

func dial(t *testing.T, ts *httptest.Server) *wsc {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return &wsc{t, conn}
}

func (c *wsc) send(v any) {
	raw, _ := json.Marshal(v)
	if err := c.conn.Write(context.Background(), websocket.MessageText, raw); err != nil {
		c.t.Fatal(err)
	}
}

func (c *wsc) recv() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, raw, err := c.conn.Read(ctx)
	if err != nil {
		return map[string]any{"_err": err.Error()}
	}
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m
}

func register(t *testing.T, ts *httptest.Server, uid string) *wsc {
	c := dial(t, ts)
	c.send(map[string]string{"type": "register", "uid": uid, "token": "tk"})
	if m := c.recv(); m["type"] != "registered" {
		t.Fatalf("register %s: %v", uid, m)
	}
	return c
}

func postTask(t *testing.T, ts *httptest.Server, body string, ctype string) apiResponse {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/v1/task", ctype, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var r apiResponse
	json.NewDecoder(res.Body).Decode(&r)
	return r
}

func TestTaskRoundTrip(t *testing.T) {
	_, ts := testServer(t, nil)
	c := register(t, ts, "dev1")
	go func() {
		m := c.recv()
		c.send(map[string]any{"type": "result", "id": m["id"], "data": map[string]any{"echo": m["data"]}})
	}()
	r := postTask(t, ts, `{"uid":"dev1","token":"tk","data":{"cmd":"run"}}`, "application/json")
	if r.Code != CodeOK || string(r.Data) != `{"echo":{"cmd":"run"}}` {
		t.Fatalf("got %+v %s", r, r.Data)
	}
}

func TestFormTask(t *testing.T) {
	_, ts := testServer(t, nil)
	c := register(t, ts, "dev1")
	go func() {
		m := c.recv()
		c.send(map[string]any{"type": "result", "id": m["id"], "data": m["data"]})
	}()
	form := url.Values{"uid": {"dev1"}, "token": {"tk"}, "data": {"你好"}}.Encode()
	r := postTask(t, ts, form, "application/x-www-form-urlencoded")
	if r.Code != CodeOK || string(r.Data) != `"你好"` {
		t.Fatalf("got %+v %s", r, r.Data)
	}
}

func TestOffline(t *testing.T) {
	_, ts := testServer(t, nil)
	r := postTask(t, ts, `{"uid":"nobody","token":"tk","data":1}`, "application/json")
	if r.Code != CodeOffline {
		t.Fatalf("got %+v", r)
	}
}

func TestTimeout(t *testing.T) {
	_, ts := testServer(t, nil)
	register(t, ts, "dev1")
	r := postTask(t, ts, `{"uid":"dev1","token":"tk","data":1,"timeout":1}`, "application/json")
	if r.Code != CodeTimeout {
		t.Fatalf("got %+v", r)
	}
}

func TestDuplicateRejected(t *testing.T) {
	_, ts := testServer(t, nil)
	register(t, ts, "dev1")
	c2 := dial(t, ts)
	c2.send(map[string]string{"type": "register", "uid": "dev1", "token": "tk"})
	if m := c2.recv(); m["type"] != "register_fail" || m["msg"] != "uid_in_use" {
		t.Fatalf("got %v", m)
	}
}

func TestStaleReplaced(t *testing.T) {
	s, ts := testServer(t, nil)
	register(t, ts, "dev1")
	s.hub.get("dev1").lastSeen.Store(time.Now().Add(-time.Hour).UnixMilli())
	register(t, ts, "dev1") // 旧连接是僵尸，新连接应能接管
}

func TestDisconnectMidTask(t *testing.T) {
	_, ts := testServer(t, func(c *Config) { c.TaskTimeout = 10 })
	c := register(t, ts, "dev1")
	go func() {
		c.recv()
		c.conn.CloseNow()
	}()
	start := time.Now()
	r := postTask(t, ts, `{"uid":"dev1","token":"tk","data":1}`, "application/json")
	if r.Code != CodeDisconnected || time.Since(start) > 3*time.Second {
		t.Fatalf("got %+v after %v", r, time.Since(start))
	}
}

func TestBanAfterThreeViolations(t *testing.T) {
	s, ts := testServer(t, nil)
	c := register(t, ts, "dev1")
	c.send("not json")
	c.recv()
	c.send(map[string]string{"type": "what"})
	c.recv()
	c.send(map[string]string{"type": "register"})
	// 第三次：被拉黑并断开
	for i := 0; i < 3; i++ {
		if m := c.recv(); m["_err"] != nil {
			break
		}
	}
	if !s.guard.Banned("uid", "dev1") {
		t.Fatal("uid should be banned")
	}
	if s.guard.Banned("ip", "127.0.0.1") {
		t.Fatal("loopback ip must not be banned")
	}
	c2 := dial(t, ts)
	c2.send(map[string]string{"type": "register", "uid": "dev1", "token": "tk"})
	if m := c2.recv(); m["msg"] != "banned" {
		t.Fatalf("got %v", m)
	}
	// 持久化后重新加载仍在
	g2, _ := newGuard(s.cfg)
	if !g2.Banned("uid", "dev1") {
		t.Fatal("ban not persisted")
	}
}

func TestIPBanViaProxyHeader(t *testing.T) {
	s, ts := testServer(t, func(c *Config) { c.TrustProxy = true })
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/task", strings.NewReader(`{"uid":"a","token":"wrong","data":1}`))
		req.Header.Set("X-Real-IP", "1.2.3.4")
		res, _ := http.DefaultClient.Do(req)
		res.Body.Close()
	}
	if !s.guard.Banned("ip", "1.2.3.4") {
		t.Fatal("ip should be banned")
	}
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/task", strings.NewReader(`{"uid":"a","token":"tk","data":1}`))
	req.Header.Set("X-Real-IP", "1.2.3.4")
	res, _ := http.DefaultClient.Do(req)
	var r apiResponse
	json.NewDecoder(res.Body).Decode(&r)
	if r.Code != CodeBanned {
		t.Fatalf("got %+v", r)
	}
}

func TestBanExpires(t *testing.T) {
	s, _ := testServer(t, nil)
	s.guard.Ban("uid", "x", "test", 1)
	if !s.guard.Banned("uid", "x") {
		t.Fatal("should be banned")
	}
	s.guard.nowFunc = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if s.guard.Banned("uid", "x") {
		t.Fatal("should have expired")
	}
}

func TestPanelAuth(t *testing.T) {
	_, ts := testServer(t, nil)
	req, _ := http.NewRequest("GET", ts.URL+"/panel/api/state", nil)
	req.Header.Set("X-Panel-Password", "pw")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	req.Header.Set("X-Panel-Password", "bad")
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 401 {
		t.Fatalf("status %d", res.StatusCode)
	}
}
