package app

import (
	"agent-web/internal/assets"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const help = `agent-web — headless browser skill

  agent-web call [JSON]          Execute a request; reads JSON from stdin if omitted
  agent-web skill                Print the installed skill instructions
  agent-web install --dir PATH   Install binary + SKILL.md into a new skill folder
  agent-web serve [options]      Run the daemon in the foreground
  agent-web stop                 Gracefully stop the local daemon
  agent-web version             Show version and embedded browser availability

Local calls auto-start a daemon. Data: AGENT_WEB_HOME (default OS config/agent-web).
Remote calls: AGENT_WEB_URL and AGENT_WEB_TOKEN. No local browser is required.
Development browser override: AGENT_WEB_BROWSER=/absolute/path/to/chrome.
See 'agent-web skill' for the JSON API and human-login flow.
`

func Run(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprint(out, help)
		return 0
	}
	var e error
	switch args[0] {
	case "version", "--version":
		_, e := fs.Stat(assets.Files, "browser/browser.zip")
		json.NewEncoder(out).Encode(map[string]any{"version": Version, "embedded_browser": e == nil})
		return 0
	case "skill":
		b, _ := assets.Files.ReadFile("SKILL.md")
		out.Write(b)
		return 0
	case "install":
		f := flag.NewFlagSet("install", flag.ContinueOnError)
		f.SetOutput(errOut)
		dir := f.String("dir", "", "destination skill folder (must not exist)")
		if e = f.Parse(args[1:]); e != nil {
			return 2
		}
		if *dir == "" {
			e = fmt.Errorf("--dir is required")
		} else {
			e = install(*dir)
		}
		if e == nil {
			json.NewEncoder(out).Encode(response(map[string]string{"installed": *dir}, nil))
			return 0
		}
	case "serve":
		f := flag.NewFlagSet("serve", flag.ContinueOnError)
		f.SetOutput(errOut)
		c := config{Home: dataHome()}
		f.StringVar(&c.Listen, "listen", "127.0.0.1:0", "HTTP listen address")
		f.StringVar(&c.PublicURL, "public-url", os.Getenv("AGENT_WEB_PUBLIC_URL"), "externally reachable origin for handoff links")
		f.StringVar(&c.Browser, "browser", os.Getenv("AGENT_WEB_BROWSER"), "override embedded browser")
		f.DurationVar(&c.TaskTTL, "task-timeout", 30*time.Minute, "inactive task expiry")
		f.DurationVar(&c.IdleTTL, "idle-timeout", 10*time.Minute, "idle account browser expiry")
		f.IntVar(&c.MaxAccounts, "max-accounts", 4, "maximum active browser instances")
		f.BoolVar(&c.NoSandbox, "no-sandbox", os.Getenv("AGENT_WEB_NO_SANDBOX") == "1", "explicitly disable Chromium sandbox (isolated containers only)")
		if e = f.Parse(args[1:]); e != nil {
			return 2
		}
		if c.MaxAccounts < 1 || c.TaskTTL < time.Second || c.IdleTTL < time.Second {
			e = fmt.Errorf("capacity and timeouts must be positive")
		} else {
			e = serve(c)
		}
		if e == nil {
			return 0
		}
	case "call", "stop":
		var b []byte
		if args[0] == "stop" {
			b = []byte(`{"op":"stop"}`)
		} else if len(args) > 1 {
			b = []byte(args[1])
		} else {
			b, e = io.ReadAll(io.LimitReader(in, 1024*1024+1))
		}
		if len(b) > 1024*1024 {
			e = fmt.Errorf("request exceeds 1 MiB")
		}
		var q Request
		if e == nil {
			e = decode(b, &q)
		}
		if e == nil {
			var st daemonState
			st, e = connection(q.Op != "stop")
			if e == nil {
				var resp Response
				resp, e = rpc(st, b, 100*time.Second)
				if e == nil && resp.OK && q.Op == "stop" && os.Getenv("AGENT_WEB_URL") == "" {
					deadline := time.Now().Add(30 * time.Second)
					for {
						current, stateErr := readState()
						if os.IsNotExist(stateErr) || (stateErr == nil && current.PID != st.PID) {
							break
						}
						if time.Now().After(deadline) {
							e = fmt.Errorf("shutdown still in progress; wait before restarting")
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
				if e == nil {
					json.NewEncoder(out).Encode(resp)
					if !resp.OK {
						return 1
					}
					return 0
				}
			}
		}
	default:
		e = fmt.Errorf("unknown command: %s", args[0])
	}
	json.NewEncoder(out).Encode(response(nil, e))
	return 1
}
func rpc(st daemonState, b []byte, timeout time.Duration) (Response, error) {
	req, e := http.NewRequest("POST", strings.TrimRight(st.URL, "/")+"/rpc", bytes.NewReader(b))
	if e != nil {
		return Response{}, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+st.Token)
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		return Response{}, e
	}
	defer res.Body.Close()
	var v Response
	e = json.NewDecoder(io.LimitReader(res.Body, 32*1024*1024)).Decode(&v)
	return v, e
}
func readState() (daemonState, error) {
	var st daemonState
	b, e := os.ReadFile(filepath.Join(dataHome(), "daemon.json"))
	if e == nil {
		e = json.Unmarshal(b, &st)
	}
	return st, e
}
func connection(start bool) (daemonState, error) {
	if u := os.Getenv("AGENT_WEB_URL"); u != "" {
		token := os.Getenv("AGENT_WEB_TOKEN")
		if token == "" {
			return daemonState{}, fmt.Errorf("AGENT_WEB_TOKEN required for remote calls")
		}
		return daemonState{URL: u, Token: token}, nil
	}
	check := func() (daemonState, bool) {
		st, e := readState()
		if e == nil {
			res, e := rpc(st, []byte(`{"op":"ping"}`), time.Second)
			if e == nil && res.OK {
				return st, true
			}
		}
		return daemonState{}, false
	}
	if st, ok := check(); ok {
		return st, nil
	}
	if !start {
		return daemonState{}, fmt.Errorf("local daemon is not running")
	}
	h := dataHome()
	if e := os.MkdirAll(h, 0700); e != nil {
		return daemonState{}, e
	}
	// Serialize concurrent first calls without holding the daemon lifetime lock.
	lock, e := os.OpenFile(filepath.Join(h, "start.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return daemonState{}, e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); e != nil {
		return daemonState{}, e
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if st, ok := check(); ok {
		return st, nil
	}
	exe, e := os.Executable()
	if e != nil {
		return daemonState{}, e
	}
	log, e := os.OpenFile(filepath.Join(h, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return daemonState{}, e
	}
	defer log.Close()
	cmd := exec.Command(exe, "serve")
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = cmd.Start(); e != nil {
		return daemonState{}, e
	}
	go cmd.Wait()
	for i := 0; i < 50; i++ {
		if st, ok := check(); ok {
			return st, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return daemonState{}, fmt.Errorf("daemon did not start; inspect %s", filepath.Join(h, "daemon.log"))
}
func install(dir string) error {
	dest, e := filepath.Abs(dir)
	if e != nil {
		return e
	}
	if _, e = os.Lstat(dest); !os.IsNotExist(e) {
		return fmt.Errorf("destination already exists or cannot be accessed: %s", dest)
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(filepath.Dir(dest), ".agent-web-install-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if e = os.Mkdir(filepath.Join(tmp, "bin"), 0755); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	src, e := os.Open(exe)
	if e != nil {
		return e
	}
	defer src.Close()
	dst, e := os.OpenFile(filepath.Join(tmp, "bin", "agent-web"), os.O_CREATE|os.O_WRONLY, 0755)
	if e != nil {
		return e
	}
	_, e = io.Copy(dst, src)
	ce := dst.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	skill, _ := assets.Files.ReadFile("SKILL.md")
	if e = os.WriteFile(filepath.Join(tmp, "SKILL.md"), skill, 0644); e != nil {
		return e
	}
	notices, _ := assets.Files.ReadFile("THIRD_PARTY_NOTICES.txt")
	if e = os.WriteFile(filepath.Join(tmp, "THIRD_PARTY_NOTICES.txt"), notices, 0644); e != nil {
		return e
	}
	if e = os.Chmod(tmp, 0755); e != nil {
		return e
	}
	return os.Rename(tmp, dest)
}
