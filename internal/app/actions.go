package app

import (
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"net/url"
	"strings"
)

func validURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fail("INVALID_URL", "navigation accepts only http:// or https:// URLs")
	}
	return nil
}
func documentID(p *rod.Page) (proto.DOMBackendNodeID, error) {
	obj, e := p.Evaluate(rod.Eval("() => document").ByObject())
	if e != nil {
		return 0, e
	}
	defer proto.RuntimeReleaseObject{ObjectID: obj.ObjectID}.Call(p)
	doc, e := (proto.DOMDescribeNode{ObjectID: obj.ObjectID}).Call(p)
	if e != nil {
		return 0, e
	}
	return doc.Node.BackendNodeID, nil
}
func observe(ps *pageState, p *rod.Page, frame string, limit int) (any, error) {
	if limit <= 0 {
		limit = 180
	}
	if limit > 600 {
		limit = 600
	}
	doc, e := documentID(p)
	if e != nil {
		return nil, e
	}
	tree, e := (proto.AccessibilityGetFullAXTree{FrameID: p.FrameID}).Call(p)
	if e != nil {
		return nil, e
	}
	ps.generation++
	ps.document = doc
	ps.frame = frame
	ps.refs = map[string]proto.DOMBackendNodeID{}
	lines := []map[string]any{}
	chars := 0
	truncated := false
	for _, n := range tree.Nodes {
		if n.Ignored || n.Role == nil {
			continue
		}
		role := n.Role.Value.Str()
		name := ""
		if n.Name != nil {
			name = n.Name.Value.Str()
		}
		if role == "InlineTextBox" || role == "generic" || role == "none" {
			continue
		}
		if name == "" && len(n.ChildIDs) > 0 && role != "textbox" && role != "combobox" {
			continue
		}
		if len(lines) >= limit || chars > 24000 {
			truncated = true
			break
		}
		if len(name) > 1200 {
			name = string([]rune(name)[:min(300, len([]rune(name)))]) + "…"
		}
		row := map[string]any{"role": role, "name": name}
		chars += len(name)
		if n.BackendDOMNodeID != 0 {
			ref := fmt.Sprintf("@%d-%d", ps.generation, len(lines)+1)
			ps.refs[ref] = n.BackendDOMNodeID
			row["ref"] = ref
		}
		for _, prop := range n.Properties {
			if prop.Name == "disabled" || prop.Name == "checked" || prop.Name == "selected" {
				row[string(prop.Name)] = prop.Value.Value.Val()
			}
		}
		lines = append(lines, row)
	}
	meta, e := p.Eval("() => ({url:location.href,title:document.title})")
	if e != nil {
		return nil, e
	}
	return map[string]any{"page": meta.Value.Val(), "frame": frame, "nodes": lines, "truncated": truncated, "hint": "refs belong to this observation and frame; observe again after navigation or stale refs"}, nil
}
func resolve(ps *pageState, p *rod.Page, frame string, a Action) (*rod.Element, error) {
	if a.Ref != "" {
		id, ok := ps.refs[a.Ref]
		if !ok || ps.frame != frame {
			return nil, fail("STALE_REF", "unknown reference or different frame; observe again")
		}
		doc, e := documentID(p)
		if e != nil {
			return nil, e
		}
		if doc != ps.document {
			return nil, fail("STALE_REF", "page navigated; observe again")
		}
		el, e := p.ElementFromNode(&proto.DOMNode{BackendNodeID: id})
		if e != nil {
			return nil, fail("STALE_REF", "element no longer exists; observe again")
		}
		return el, nil
	}
	if a.Selector == "" {
		return nil, fail("INVALID_ACTION", "selector or ref required")
	}
	return p.Element(a.Selector)
}
func perform(ps *pageState, p *rod.Page, frame string, a Action) error {
	switch a.Type {
	case "navigate":
		if e := validURL(a.URL); e != nil {
			return e
		}
		ps.refs = nil
		return p.Navigate(a.URL)
	case "text":
		return p.InsertText(a.Text)
	case "key":
		return key(p, a.Key)
	case "scroll":
		return (proto.InputDispatchMouseEvent{Type: "mouseWheel", X: 640, Y: 400, DeltaX: a.X, DeltaY: a.Y}).Call(p)
	case "click_xy":
		return click(p, a.X, a.Y)
	case "click", "fill", "select", "wait":
		el, e := resolve(ps, p, frame, a)
		if e != nil {
			return e
		}
		switch a.Type {
		case "click":
			return el.Click(proto.InputMouseButtonLeft, 1)
		case "fill":
			if e = el.SelectAllText(); e != nil {
				return e
			}
			return el.Input(a.Text)
		case "wait":
			return el.WaitVisible()
		case "select":
			return el.Select([]string{a.Value}, true, rod.SelectorTypeCSSSector)
		}
	}
	return fail("INVALID_ACTION", "unsupported action: "+a.Type)
}
func click(p *rod.Page, x, y float64) error {
	if x < 0 || y < 0 || x > 10000 || y > 10000 {
		return fail("INVALID_ACTION", "invalid coordinates")
	}
	if e := (proto.InputDispatchMouseEvent{Type: "mousePressed", X: x, Y: y, Button: "left", ClickCount: 1}).Call(p); e != nil {
		return e
	}
	return (proto.InputDispatchMouseEvent{Type: "mouseReleased", X: x, Y: y, Button: "left", ClickCount: 1}).Call(p)
}
func key(p *rod.Page, k string) error {
	codes := map[string]int{"Enter": 13, "Tab": 9, "Backspace": 8, "Delete": 46, "Escape": 27, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Home": 36, "End": 35, "PageUp": 33, "PageDown": 34}
	if strings.EqualFold(k, "SelectAll") {
		return (proto.InputDispatchKeyEvent{Type: "keyDown", Key: "a", Commands: []string{"selectAll"}}).Call(p)
	}
	c, ok := codes[k]
	if !ok {
		return fail("INVALID_ACTION", "unsupported key; use text for character input")
	}
	if e := (proto.InputDispatchKeyEvent{Type: "keyDown", Key: k, WindowsVirtualKeyCode: c}).Call(p); e != nil {
		return e
	}
	return (proto.InputDispatchKeyEvent{Type: "keyUp", Key: k, WindowsVirtualKeyCode: c}).Call(p)
}
