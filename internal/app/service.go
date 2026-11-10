package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type service struct {
	mu       sync.Mutex
	cfg      config
	accounts map[string]*account
	tasks    map[string]*task
	baseURL  string
}

var accountName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func newService(c config) *service {
	return &service{cfg: c, accounts: map[string]*account{}, tasks: map[string]*task{}}
}

func (s *service) openAccount(name string) (*account, error) {
	if a := s.accounts[name]; a != nil {
		if _, e := a.browser.Timeout(2 * time.Second).Version(); e == nil {
			return a, nil
		}
		if a.active != "" {
			return nil, fail("BROWSER_LOST", "browser crashed; finish the interrupted task before starting another")
		}
		a.launcher.Kill()
		delete(s.accounts, name)
	}
	if len(s.accounts) >= s.cfg.MaxAccounts {
		var oldest *account
		for _, a := range s.accounts {
			if a.active == "" && (oldest == nil || a.touched.Before(oldest.touched)) {
				oldest = a
			}
		}
		if oldest == nil {
			return nil, fail("CAPACITY", "all browser slots are busy")
		}
		if e := s.closeAccount(oldest); e != nil {
			return nil, e
		}
	}
	path, e := browserPath(s.cfg.Home, s.cfg.Browser)
	if e != nil {
		return nil, e
	}
	s.cfg.Browser = path
	profile := filepath.Join(s.cfg.Home, "accounts", name, "profile")
	if e = os.MkdirAll(profile, 0700); e != nil {
		return nil, e
	}
	l := launcher.New().Bin(path).UserDataDir(profile).Headless(true)
	// Retain Chromium isolation and sandbox defaults rather than rod's test presets.
	l.Delete("disable-site-isolation-trials").Delete("disable-features").Delete("disable-ipc-flooding-protection")
	l.NoSandbox(s.cfg.NoSandbox).Set("remote-debugging-address", "127.0.0.1")
	u, e := l.Launch()
	if e != nil {
		return nil, e
	}
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if e = b.Connect(); e != nil {
		l.Kill()
		return nil, e
	}
	a := &account{name: name, browser: b, launcher: l, touched: time.Now()}
	s.accounts[name] = a
	// Persistent profile preserves localStorage/IndexedDB. Explicit cookie checkpoints
	// also restore session cookies across clean browser restarts.
	if raw, err := os.ReadFile(s.cookiePath(name)); err == nil {
		var cookies []*proto.NetworkCookie
		if e = json.Unmarshal(raw, &cookies); e == nil {
			params := make([]*proto.NetworkCookieParam, 0, len(cookies))
			for _, c := range cookies {
				if !c.Session && float64(c.Expires) < float64(time.Now().Unix()) {
					continue
				}
				blob, _ := json.Marshal(c)
				var p proto.NetworkCookieParam
				if e = json.Unmarshal(blob, &p); e != nil {
					break
				}
				if c.Session {
					p.Expires = 0
				}
				params = append(params, &p)
			}
			if e == nil && len(params) > 0 {
				e = b.Timeout(10 * time.Second).SetCookies(params)
			}
		}
		if e != nil {
			b.Close()
			l.Kill()
			delete(s.accounts, name)
			return nil, fmt.Errorf("restore cookies: %w", e)
		}
	} else if !os.IsNotExist(err) {
		b.Close()
		l.Kill()
		delete(s.accounts, name)
		return nil, err
	}
	// Previous browser crashes can restore tabs; tasks never adopt those implicitly.
	pages, _ := b.Pages()
	for _, p := range pages {
		_ = p.Close()
	}
	return a, nil
}
func (s *service) cookiePath(name string) string {
	return filepath.Join(s.cfg.Home, "accounts", name, "cookies.json")
}
func (s *service) checkpoint(a *account) error {
	cookies, e := a.browser.Timeout(5 * time.Second).GetCookies()
	if e != nil {
		return e
	}
	return atomicJSON(s.cookiePath(a.name), cookies)
}
func (s *service) closeAccount(a *account) error {
	e := s.checkpoint(a)
	_ = a.browser.Timeout(5 * time.Second).Close()
	a.launcher.Kill()
	delete(s.accounts, a.name)
	return e
}
func (s *service) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.accounts {
		if e := s.closeAccount(a); e != nil {
			fmt.Fprintln(os.Stderr, "shutdown checkpoint:", e)
		}
	}
}
func (s *service) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.State == "finished" || t.State == "expired" {
			if time.Since(t.Touched) > time.Hour {
				delete(s.tasks, t.ID)
			}
			continue
		}
		if (t.State == "awaiting_user" && time.Now().After(t.HumanUntil)) || time.Since(t.Touched) > s.cfg.TaskTTL {
			_ = s.finish(t)
			t.State = "expired"
			t.Touched = time.Now()
		}
	}
	for _, a := range s.accounts {
		if a.active == "" && time.Since(a.touched) > s.cfg.IdleTTL {
			_ = s.closeAccount(a)
		}
	}
}
func (s *service) finish(t *task) error {
	a := s.accounts[t.Account]
	var saveErr error
	if a != nil {
		saveErr = s.checkpoint(a)
		pages, _ := a.browser.Pages()
		for _, p := range pages {
			_ = p.Timeout(3 * time.Second).Close()
		}
		a.active = ""
		a.touched = time.Now()
	}
	t.State = "finished"
	t.HumanToken = ""
	t.Pages = map[string]*pageState{}
	t.Touched = time.Now()
	if saveErr != nil {
		return fail("STATE_SAVE_FAILED", "task released but session cookie checkpoint failed: "+saveErr.Error())
	}
	return nil
}
func (s *service) syncPages(t *task) error {
	a := s.accounts[t.Account]
	if a == nil {
		return fail("BROWSER_LOST", "browser unavailable")
	}
	pages, e := a.browser.Pages()
	if e != nil {
		return e
	}
	found := map[proto.TargetTargetID]bool{}
	for _, p := range pages {
		found[p.TargetID] = true
		known := false
		for _, ps := range t.Pages {
			if ps.page.TargetID == p.TargetID {
				known = true
				break
			}
		}
		if !known {
			t.NextPage++
			t.Pages[fmt.Sprintf("p%d", t.NextPage)] = &pageState{page: p}
		}
	}
	for id, ps := range t.Pages {
		if !found[ps.page.TargetID] {
			delete(t.Pages, id)
		}
	}
	return nil
}
func (s *service) current(t *task, id string) (*pageState, error) {
	if e := s.syncPages(t); e != nil {
		return nil, e
	}
	if id == "" {
		id = "p1"
	}
	if p := t.Pages[id]; p != nil {
		return p, nil
	}
	return nil, fail("PAGE_NOT_FOUND", "page does not exist; use tabs to list pages")
}
func (s *service) dispatch(r Request) (any, error) {
	if r.Op == "ping" {
		return map[string]any{"version": Version, "pid": os.Getpid()}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Op == "start" {
		name := r.Account
		if name == "" {
			name = "default"
		}
		if !accountName.MatchString(name) {
			return nil, fail("INVALID_ACCOUNT", "account must use 1–64 letters, digits, _ or -")
		}
		if a := s.accounts[name]; a != nil && a.active != "" {
			return nil, fail("ACCOUNT_BUSY", "account already has an active task: "+a.active)
		}
		a, e := s.openAccount(name)
		if e != nil {
			return nil, e
		}
		p, e := a.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if e != nil {
			return nil, e
		}
		if e = p.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 1280, Height: 800, DeviceScaleFactor: 1}); e != nil {
			p.Close()
			return nil, e
		}
		if r.URL != "" {
			if e = validURL(r.URL); e == nil {
				e = p.Timeout(20 * time.Second).Navigate(r.URL)
			}
			if e != nil {
				p.Close()
				return nil, e
			}
		}
		id := secret()[:16]
		t := &task{ID: id, Account: name, State: "running", Created: time.Now(), Touched: time.Now(), NextPage: 1, Pages: map[string]*pageState{"p1": {page: p}}}
		s.tasks[id] = t
		a.active = id
		v := t.view()
		v["page"] = "p1"
		v["browser_pid"] = a.launcher.PID()
		return v, nil
	}
	t := s.tasks[r.Task]
	if t == nil {
		return nil, fail("TASK_NOT_FOUND", "unknown task; tasks do not survive daemon restart")
	}
	if r.Op == "status" {
		return t.view(), nil
	}
	if t.State == "finished" || t.State == "expired" {
		return nil, fail("TASK_ENDED", "task already ended")
	}
	t.Touched = time.Now()
	if r.Op == "finish" {
		e := s.finish(t)
		return t.view(), e
	}
	if r.Op == "handoff" {
		t.State = "awaiting_user"
		t.Reason = r.Reason
		t.HumanToken = secret()
		t.HumanUntil = time.Now().Add(15 * time.Minute)
		return map[string]any{"task": t.ID, "state": t.State, "url": s.baseURL + "/handoff/" + t.ID + "#" + t.HumanToken, "expires": t.HumanUntil}, nil
	}
	if t.State == "awaiting_user" {
		return nil, fail("NEEDS_HUMAN", "user owns this session; wait for completion via status")
	}
	if r.Op == "tabs" {
		if e := s.syncPages(t); e != nil {
			return nil, e
		}
		out := map[string]any{}
		for id, ps := range t.Pages {
			info, e := ps.page.Timeout(5 * time.Second).Info()
			if e == nil {
				out[id] = map[string]any{"url": info.URL, "title": info.Title}
			}
		}
		return out, nil
	}
	if r.Op == "new_page" {
		a := s.accounts[t.Account]
		p, e := a.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if e != nil {
			return nil, e
		}
		_ = p.SetViewport(&proto.EmulationSetDeviceMetricsOverride{Width: 1280, Height: 800, DeviceScaleFactor: 1})
		if r.URL != "" {
			if e = validURL(r.URL); e == nil {
				e = p.Timeout(20 * time.Second).Navigate(r.URL)
			}
			if e != nil {
				p.Close()
				return nil, e
			}
		}
		t.NextPage++
		id := fmt.Sprintf("p%d", t.NextPage)
		t.Pages[id] = &pageState{page: p}
		return map[string]string{"page": id}, nil
	}
	ps, e := s.current(t, r.Page)
	if e != nil {
		return nil, e
	}
	p := ps.page.Timeout(15 * time.Second)
	if r.Op == "close_page" {
		e = p.Close()
		return map[string]bool{"closed": e == nil}, e
	}
	if r.Frame != "" {
		el, err := p.Element(r.Frame)
		if err != nil {
			return nil, err
		}
		p, e = el.Frame()
		if e != nil {
			return nil, e
		}
	}
	switch r.Op {
	case "observe":
		return observe(ps, p, r.Frame, r.Limit)
	case "eval":
		if r.Script == "" {
			return nil, fail("INVALID_REQUEST", "script must be a JavaScript function")
		}
		result, err := p.Eval(r.Script)
		if err != nil {
			return nil, err
		}
		return result.Value.Val(), nil
	case "screenshot":
		data, err := ps.page.Timeout(10*time.Second).Screenshot(false, &proto.PageCaptureScreenshot{Format: "png"})
		if err != nil {
			return nil, err
		}
		return map[string]any{"mime": "image/png", "base64": data}, nil
	case "act":
		if len(r.Actions) == 0 || len(r.Actions) > 50 {
			return nil, fail("INVALID_REQUEST", "actions must contain 1–50 items")
		}
		out := []any{}
		for i, act := range r.Actions {
			if e := perform(ps, p, r.Frame, act); e != nil {
				return map[string]any{"completed": i, "failed_index": i, "results": out, "retry": "inspect page before retrying; earlier actions may have committed"}, e
			}
			out = append(out, map[string]any{"index": i, "type": act.Type, "ok": true})
		}
		if e := s.checkpoint(s.accounts[t.Account]); e != nil {
			return map[string]any{"completed": len(out), "results": out}, fail("STATE_SAVE_FAILED", e.Error())
		}
		return map[string]any{"completed": len(out), "results": out}, nil
	default:
		return nil, fail("INVALID_OP", "unknown operation")
	}
}
