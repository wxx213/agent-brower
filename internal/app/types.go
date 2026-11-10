package app

import (
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"time"
)

type Request struct {
	Op      string   `json:"op"`
	Account string   `json:"account,omitempty"`
	Task    string   `json:"task,omitempty"`
	Page    string   `json:"page,omitempty"`
	URL     string   `json:"url,omitempty"`
	Frame   string   `json:"frame,omitempty"`
	Actions []Action `json:"actions,omitempty"`
	Script  string   `json:"script,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}
type Action struct {
	Type     string  `json:"type"`
	Selector string  `json:"selector,omitempty"`
	Ref      string  `json:"ref,omitempty"`
	Text     string  `json:"text,omitempty"`
	URL      string  `json:"url,omitempty"`
	Key      string  `json:"key,omitempty"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	Value    string  `json:"value,omitempty"`
}
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f *Failure) Error() string     { return f.Message }
func fail(code, msg string) *Failure { return &Failure{code, msg} }

type Response struct {
	OK     bool     `json:"ok"`
	Result any      `json:"result,omitempty"`
	Error  *Failure `json:"error,omitempty"`
}

func response(v any, e error) Response {
	if e == nil {
		return Response{OK: true, Result: v}
	}
	if f, ok := e.(*Failure); ok {
		return Response{Result: v, Error: f}
	}
	return Response{Result: v, Error: fail("BROWSER_ERROR", e.Error())}
}

type daemonState struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}
type config struct {
	Home, Browser, Listen, PublicURL string
	TaskTTL, IdleTTL                 time.Duration
	MaxAccounts                      int
	NoSandbox                        bool
}
type account struct {
	name     string
	browser  *rod.Browser
	launcher *launcher.Launcher
	active   string
	touched  time.Time
}
type pageState struct {
	page       *rod.Page
	refs       map[string]proto.DOMBackendNodeID
	document   proto.DOMBackendNodeID
	frame      string
	generation int
}
type task struct {
	ID, Account, State, Reason, HumanToken string
	Created, Touched, HumanUntil           time.Time
	Pages                                  map[string]*pageState
	NextPage                               int
}

func (t *task) view() map[string]any {
	return map[string]any{"task": t.ID, "account": t.Account, "state": t.State, "created": t.Created, "last_active": t.Touched, "reason": t.Reason}
}
func decode(b []byte, dst any) error {
	if e := json.Unmarshal(b, dst); e != nil {
		return fail("INVALID_JSON", fmt.Sprintf("invalid JSON: %v", e))
	}
	return nil
}
