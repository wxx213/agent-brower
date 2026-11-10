package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandoffScopeAndExpiry(t *testing.T) {
	s := newService(config{})
	s.tasks["one"] = &task{ID: "one", State: "awaiting_user", HumanToken: "secret-one", HumanUntil: time.Now().Add(time.Minute)}
	s.tasks["two"] = &task{ID: "two", State: "awaiting_user", HumanToken: "secret-two", HumanUntil: time.Now().Add(time.Minute)}
	for _, tc := range []struct{ name, path, token string }{
		{"other task", "/human/two/tabs", "secret-one"},
		{"wrong token", "/human/one/frame", "bad"},
		{"missing task", "/human/missing/input", "secret-one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			s.human(w, r)
			if w.Code != 401 {
				t.Fatalf("expected 401, got %d", w.Code)
			}
		})
	}
	s.tasks["one"].HumanUntil = time.Now().Add(-time.Second)
	r := httptest.NewRequest("GET", "/human/one/tabs", nil)
	r.Header.Set("Authorization", "Bearer secret-one")
	w := httptest.NewRecorder()
	s.human(w, r)
	if w.Code != 401 {
		t.Fatal("expired link accepted")
	}
}

func TestStrictRequest(t *testing.T) {
	for _, body := range []string{`{"op":"ping","typo":true}`, `{"op":"ping"}{"op":"stop"}`, `{`} {
		r := httptest.NewRequest("POST", "/rpc", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var q Request
		if readRequest(httptest.NewRecorder(), r, &q) == nil {
			t.Fatalf("accepted invalid request %q", body)
		}
	}
}

func TestPartialResultsSurviveFailure(t *testing.T) {
	b, e := json.Marshal(response(map[string]int{"completed": 2}, fail("STALE_REF", "refresh")))
	if e != nil {
		t.Fatal(e)
	}
	var v Response
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	if v.OK || v.Error.Code != "STALE_REF" || v.Result.(map[string]any)["completed"] != float64(2) {
		t.Fatal(string(b))
	}
}

func TestAccountAndNavigationBoundaries(t *testing.T) {
	for _, n := range []string{"../other", "/tmp/x", "", "a/b", "a b"} {
		if accountName.MatchString(n) {
			t.Fatal(n)
		}
	}
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "data:text/html,hello", "http://"} {
		if validURL(u) == nil {
			t.Fatal(u)
		}
	}
	if validURL("http://127.0.0.1:9999/test") != nil {
		t.Fatal("local test URLs must remain available")
	}
}

func TestTaskExpiryRevokesHandoff(t *testing.T) {
	s := newService(config{TaskTTL: time.Minute, IdleTTL: time.Minute})
	t1 := &task{ID: "idle", State: "running", Touched: time.Now().Add(-2 * time.Minute)}
	t2 := &task{ID: "human", State: "awaiting_user", HumanToken: "secret", HumanUntil: time.Now().Add(-time.Second), Touched: time.Now()}
	s.tasks[t1.ID] = t1
	s.tasks[t2.ID] = t2
	s.sweep()
	for _, task := range []*task{t1, t2} {
		if task.State != "expired" || task.HumanToken != "" {
			t.Fatal("expired task retained control", task)
		}
	}
}

func TestInstallerDoesNotReplaceDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "skill")
	if e := os.Symlink(filepath.Join(dir, "missing"), destination); e != nil {
		t.Fatal(e)
	}
	if e := install(destination); e == nil {
		t.Fatal("installer replaced an existing symlink")
	}
	if st, e := os.Lstat(destination); e != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("original symlink changed")
	}
}

func TestHealthCheckDoesNotWaitForBrowserAction(t *testing.T) {
	s := newService(config{})
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.dispatch(Request{Op: "ping"}) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("health check blocked behind browser operation")
	}
}
