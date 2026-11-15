package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"net/http"
	"time"
)

//go:embed panel.html
var panelHTML []byte

func (s *Server) registerPanel() {
	s.mux.HandleFunc("GET /panel", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(panelHTML)
	})
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/panel", http.StatusFound)
	})
	s.mux.HandleFunc("GET /panel/api/state", s.panelAuth(s.panelState))
	s.mux.HandleFunc("POST /panel/api/kick", s.panelAuth(s.panelKick))
	s.mux.HandleFunc("POST /panel/api/ban", s.panelAuth(s.panelBan))
	s.mux.HandleFunc("POST /panel/api/unban", s.panelAuth(s.panelUnban))
}

// panelAuth 校验 X-Panel-Password。口令错误也算 IP 违规，防爆破。
func (s *Server) panelAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		if s.guard.Banned("ip", ip) {
			http.Error(w, "banned", http.StatusForbidden)
			return
		}
		got := r.Header.Get("X-Panel-Password")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.PanelPassword)) != 1 {
			s.violate(ip, "", "面板口令错误")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writePanelJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) panelState(w http.ResponseWriter, _ *http.Request) {
	bans, violations := s.guard.Snapshot()
	st := &s.hub.stats
	writePanelJSON(w, map[string]any{
		"version":    version,
		"now":        time.Now().Unix(),
		"started":    s.started.Unix(),
		"clients":    s.hub.list(),
		"bans":       bans,
		"violations": violations,
		"limit":      s.cfg.ViolationLimit,
		"stats": map[string]int64{
			"total": st.Total.Load(), "ok": st.OK.Load(), "offline": st.Offline.Load(),
			"timeout": st.Timeout.Load(), "disconnected": st.Disconnected.Load(),
		},
	})
}

func (s *Server) panelKick(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UID string `json:"uid"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if c := s.hub.get(req.UID); c != nil {
		_ = c.send(map[string]string{"type": "error", "msg": "kicked"})
		c.close("kicked")
	}
	writePanelJSON(w, map[string]bool{"ok": true})
}

func (s *Server) panelBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		Value   string `json:"value"`
		Minutes int    `json:"minutes"`
		Reason  string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if (req.Kind != "ip" && req.Kind != "uid") || req.Value == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Reason == "" {
		req.Reason = "手动拉黑"
	}
	s.guard.Ban(req.Kind, req.Value, req.Reason, req.Minutes)
	writePanelJSON(w, map[string]bool{"ok": true})
}

func (s *Server) panelUnban(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	writePanelJSON(w, map[string]bool{"ok": s.guard.Unban(req.Key)})
}
