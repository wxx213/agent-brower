package app

import (
	"agent-web/internal/assets"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod/lib/proto"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func match(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func readRequest(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Method != "POST" {
		return fail("METHOD", "POST required")
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return fail("CONTENT_TYPE", "application/json required")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		return fail("INVALID_JSON", e.Error())
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return fail("INVALID_JSON", "expected one JSON object")
	}
	return nil
}
func serve(c config) error {
	if e := os.MkdirAll(c.Home, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(c.Home, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fail("ALREADY_RUNNING", "a daemon already owns this data directory")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ln, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return e
	}
	defer ln.Close()
	token := os.Getenv("AGENT_WEB_TOKEN")
	if token == "" {
		token = secret()
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	endpoint := "http://" + net.JoinHostPort(host, port)
	state := daemonState{URL: endpoint, Token: token, PID: os.Getpid()}
	s := newService(c)
	s.baseURL = strings.TrimRight(c.PublicURL, "/")
	if s.baseURL == "" {
		s.baseURL = endpoint
	}
	defer os.Remove(filepath.Join(c.Home, "daemon.json"))
	defer s.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if !match(bearer(r), token) {
			w.WriteHeader(401)
			jsonReply(w, response(nil, fail("UNAUTHORIZED", "bearer token required")))
			return
		}
		var q Request
		if e := readRequest(w, r, &q); e != nil {
			jsonReply(w, response(nil, e))
			return
		}
		if q.Op == "stop" {
			jsonReply(w, response(map[string]bool{"stopping": true}, nil))
			go stop()
			return
		}
		v, e := s.dispatch(q)
		jsonReply(w, response(v, e))
	})
	mux.HandleFunc("/handoff/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		b, _ := assets.Files.ReadFile("handoff.html")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/human/", s.human)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	if e = atomicJSON(filepath.Join(c.Home, "daemon.json"), state); e != nil {
		return e
	}
	fmt.Fprintln(os.Stderr, "agent-web listening at", endpoint, "(credentials in private daemon.json)")
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.sweep()
			}
		}
	}()
	e = server.Serve(ln)
	if e == http.ErrServerClosed {
		<-drained
		return nil
	}
	return e
}

type humanInput struct {
	Type      string  `json:"type"`
	Page      string  `json:"page"`
	Text      string  `json:"text"`
	Key       string  `json:"key"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Button    string  `json:"button"`
	Buttons   int     `json:"buttons"`
	Modifiers int     `json:"modifiers"`
}

func (s *service) human(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/human/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	t := s.tasks[parts[0]]
	if t == nil || t.State != "awaiting_user" || time.Now().After(t.HumanUntil) || !match(bearer(r), t.HumanToken) {
		w.WriteHeader(401)
		jsonReply(w, response(nil, fail("HANDOFF_EXPIRED", "handoff expired or finished")))
		return
	}
	t.Touched = time.Now()
	if parts[1] == "tabs" && r.Method == "GET" {
		if e := s.syncPages(t); e != nil {
			jsonReply(w, response(nil, e))
			return
		}
		out := map[string]any{}
		for id, p := range t.Pages {
			info, e := p.page.Timeout(3 * time.Second).Info()
			if e == nil {
				out[id] = map[string]string{"title": info.Title, "url": info.URL}
			}
		}
		jsonReply(w, response(map[string]any{"pages": out, "reason": t.Reason}, nil))
		return
	}
	if parts[1] == "frame" && r.Method == "GET" {
		ps, e := s.current(t, r.URL.Query().Get("page"))
		if e != nil {
			jsonReply(w, response(nil, e))
			return
		}
		quality := 65
		b, e := ps.page.Timeout(5*time.Second).Screenshot(false, &proto.PageCaptureScreenshot{Format: "jpeg", Quality: &quality})
		if e != nil {
			jsonReply(w, response(nil, e))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
		return
	}
	if parts[1] != "input" {
		http.NotFound(w, r)
		return
	}
	var in humanInput
	if e := readRequest(w, r, &in); e != nil {
		jsonReply(w, response(nil, e))
		return
	}
	if in.Type == "done" {
		if e := s.checkpoint(s.accounts[t.Account]); e != nil {
			jsonReply(w, response(nil, e))
			return
		}
		t.State = "running"
		t.HumanToken = ""
		t.Reason = "user returned control; verify login before proceeding"
		jsonReply(w, response(t.view(), nil))
		return
	}
	ps, e := s.current(t, in.Page)
	if e != nil {
		jsonReply(w, response(nil, e))
		return
	}
	p := ps.page.Timeout(5 * time.Second)
	switch in.Type {
	case "text":
		e = p.InsertText(in.Text)
	case "key":
		e = key(p, in.Key)
	case "scroll":
		e = (proto.InputDispatchMouseEvent{Type: "mouseWheel", X: 640, Y: 400, DeltaX: in.X, DeltaY: in.Y}).Call(p)
	case "down", "up", "move":
		if in.X < 0 || in.X > 1280 || in.Y < 0 || in.Y > 800 {
			e = fail("INVALID_INPUT", "coordinates outside viewport")
			break
		}
		typ := proto.InputDispatchMouseEventType("mouseMoved")
		if in.Type == "down" {
			typ = "mousePressed"
		}
		if in.Type == "up" {
			typ = "mouseReleased"
		}
		button := proto.InputMouseButton(in.Button)
		if button != "left" && button != "right" && button != "none" {
			button = "left"
		}
		e = (proto.InputDispatchMouseEvent{Type: typ, X: in.X, Y: in.Y, Button: button, Buttons: &in.Buttons, ClickCount: 1, Modifiers: in.Modifiers}).Call(p)
	default:
		e = fail("INVALID_INPUT", "unknown input type")
	}
	jsonReply(w, response(map[string]bool{"sent": e == nil}, e))
}
