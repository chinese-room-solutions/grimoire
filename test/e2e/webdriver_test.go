//go:build e2e

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// A minimal W3C WebDriver client over chromedriver's plain HTTP+JSON protocol —
// just what the smoke suite needs (session, navigate, find, click, keys,
// actions, execute, screenshot, console log). Deliberately no third-party
// webdriver dependency: the protocol is a handful of JSON POSTs.

// webElementKey is the W3C web-element identifier key in element references.
const webElementKey = "element-6066-11e4-a52e-4f735466cecf"

// W3C key codepoints for the actions/sendKeys APIs.
const (
	keyControl = "\uE009"
	keyEscape  = "\uE00C"
	keyDelete  = "\uE017"
)

// driver is one browser session against a running chromedriver.
type driver struct {
	base string // chromedriver base URL, e.g. http://127.0.0.1:9515
	sid  string
	hc   *http.Client
}

// wdError is the decoded W3C error payload of a non-2xx response.
type wdError struct {
	Err     string `json:"error"`
	Message string `json:"message"`
}

func (e *wdError) Error() string { return e.Err + ": " + e.Message }

// newSession opens a headless Chrome session. chromeBinary may be "" to let
// chromedriver locate the browser itself; profileDir isolates the Chrome
// profile (scratch, never the user's).
func newSession(base, chromeBinary, profileDir string) (*driver, error) {
	chromeOpts := map[string]any{
		"args": []string{
			"--headless=new",
			"--disable-gpu",
			"--no-sandbox",
			"--disable-dev-shm-usage",
			"--window-size=1400,900",
			"--user-data-dir=" + profileDir,
		},
	}
	if chromeBinary != "" {
		chromeOpts["binary"] = chromeBinary
	}
	d := &driver{base: base, hc: &http.Client{Timeout: 60 * time.Second}}
	val, err := d.do(http.MethodPost, "/session", map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"browserName":        "chrome",
				"goog:chromeOptions": chromeOpts,
				// Non-W3C but honored by chromedriver: browser console log access.
				"goog:loggingPrefs": map[string]string{"browser": "ALL"},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}
	var resp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(val, &resp); err != nil || resp.SessionID == "" {
		return nil, fmt.Errorf("no sessionId in response %s", val)
	}
	d.sid = resp.SessionID
	return d, nil
}

// quit ends the session (chromedriver tears down its Chrome).
func (d *driver) quit() {
	_, _ = d.do(http.MethodDelete, "/session/"+d.sid, nil)
}

// do performs one WebDriver request and returns the "value" payload. A non-2xx
// response is decoded into the W3C error shape.
func (d *driver) do(method, path string, body any) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	} else if method == http.MethodPost {
		rd = bytes.NewReader([]byte("{}")) // W3C POSTs require a JSON body.
	}
	req, err := http.NewRequest(method, d.base+path, rd)
	if err != nil {
		return nil, err
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%s %s: bad response %q", method, path, raw)
	}
	if resp.StatusCode >= 400 {
		var werr wdError
		_ = json.Unmarshal(env.Value, &werr)
		if werr.Err == "" {
			werr.Err = resp.Status
		}
		return nil, fmt.Errorf("%s %s: %w", method, path, &werr)
	}
	return env.Value, nil
}

// sess prefixes a session-scoped path.
func (d *driver) sess(path string) string { return "/session/" + d.sid + path }

// navigate loads url and waits for the document (normal page-load strategy).
func (d *driver) navigate(url string) error {
	_, err := d.do(http.MethodPost, d.sess("/url"), map[string]string{"url": url})
	return err
}

// refresh reloads the current page.
func (d *driver) refresh() error {
	_, err := d.do(http.MethodPost, d.sess("/refresh"), nil)
	return err
}

// find returns the element id for the first CSS match, or an error (including
// "no such element") — poll around it for async UI.
func (d *driver) find(sel string) (string, error) {
	val, err := d.do(http.MethodPost, d.sess("/element"),
		map[string]string{"using": "css selector", "value": sel})
	if err != nil {
		return "", err
	}
	return elementID(val)
}

// findAll returns the element ids of every CSS match (possibly none).
func (d *driver) findAll(sel string) ([]string, error) {
	val, err := d.do(http.MethodPost, d.sess("/elements"),
		map[string]string{"using": "css selector", "value": sel})
	if err != nil {
		return nil, err
	}
	var refs []map[string]string
	if err := json.Unmarshal(val, &refs); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		if id := r[webElementKey]; id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// text returns an element's rendered text.
func (d *driver) text(id string) (string, error) {
	val, err := d.do(http.MethodGet, d.sess("/element/"+id+"/text"), nil)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(val, &s); err != nil {
		return "", err
	}
	return s, nil
}

// findIn finds by CSS below a parent element.
func (d *driver) findIn(parent, sel string) (string, error) {
	val, err := d.do(http.MethodPost, d.sess("/element/"+parent+"/element"),
		map[string]string{"using": "css selector", "value": sel})
	if err != nil {
		return "", err
	}
	return elementID(val)
}

func elementID(val json.RawMessage) (string, error) {
	var ref map[string]string
	if err := json.Unmarshal(val, &ref); err != nil {
		return "", err
	}
	id := ref[webElementKey]
	if id == "" {
		return "", fmt.Errorf("no element id in %s", val)
	}
	return id, nil
}

// click clicks an element by id.
func (d *driver) click(id string) error {
	_, err := d.do(http.MethodPost, d.sess("/element/"+id+"/click"), nil)
	return err
}

// clickSel finds the first CSS match and clicks it.
func (d *driver) clickSel(sel string) error {
	id, err := d.find(sel)
	if err != nil {
		return err
	}
	return d.click(id)
}

// sendKeys types text into an element (focuses it first, per spec).
func (d *driver) sendKeys(id, text string) error {
	_, err := d.do(http.MethodPost, d.sess("/element/"+id+"/value"),
		map[string]string{"text": text})
	return err
}

// exec runs script synchronously in the page and returns its decoded result.
func (d *driver) exec(script string, args ...any) (any, error) {
	if args == nil {
		args = []any{}
	}
	val, err := d.do(http.MethodPost, d.sess("/execute/sync"),
		map[string]any{"script": script, "args": args})
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(val, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// evalBool runs `return <expr>` and reports whether it yielded true.
func (d *driver) evalBool(expr string) (bool, error) {
	out, err := d.exec("return !!(" + expr + ");")
	if err != nil {
		return false, err
	}
	b, _ := out.(bool)
	return b, nil
}

// evalString runs `return <expr>` and returns the result as a string.
func (d *driver) evalString(expr string) (string, error) {
	out, err := d.exec("return String(" + expr + ");")
	if err != nil {
		return "", err
	}
	s, _ := out.(string)
	return s, nil
}

// keyChord presses keys together (down in order, up in reverse) — e.g. Ctrl+S is
// keyChord(keyControl, "s"). Keys go to the focused element.
func (d *driver) keyChord(keys ...string) error {
	var acts []map[string]any
	for _, k := range keys {
		acts = append(acts, map[string]any{"type": "keyDown", "value": k})
	}
	for i := len(keys) - 1; i >= 0; i-- {
		acts = append(acts, map[string]any{"type": "keyUp", "value": keys[i]})
	}
	return d.performActions([]map[string]any{{"type": "key", "id": "kb", "actions": acts}})
}

// hover moves the mouse pointer over an element's center (drives CSS :hover).
func (d *driver) hover(id string) error {
	return d.performActions([]map[string]any{pointerSeq(
		map[string]any{"type": "pointerMove", "duration": 0, "x": 0, "y": 0,
			"origin": map[string]string{webElementKey: id}},
	)})
}

// doubleClick performs a real double click on an element (two down/up pairs at
// one spot), which the browser reports as click, click, dblclick.
func (d *driver) doubleClick(id string) error {
	return d.performActions([]map[string]any{pointerSeq(
		map[string]any{"type": "pointerMove", "duration": 0, "x": 0, "y": 0,
			"origin": map[string]string{webElementKey: id}},
		map[string]any{"type": "pointerDown", "button": 0},
		map[string]any{"type": "pointerUp", "button": 0},
		map[string]any{"type": "pointerDown", "button": 0},
		map[string]any{"type": "pointerUp", "button": 0},
	)})
}

func pointerSeq(acts ...map[string]any) map[string]any {
	return map[string]any{
		"type": "pointer", "id": "mouse",
		"parameters": map[string]string{"pointerType": "mouse"},
		"actions":    acts,
	}
}

// performActions posts a W3C actions sequence and then releases input state, so
// a chord can't leave a modifier held for the next gesture.
func (d *driver) performActions(actions []map[string]any) error {
	_, err := d.do(http.MethodPost, d.sess("/actions"), map[string]any{"actions": actions})
	if rerr := d.releaseActions(); err == nil {
		err = rerr
	}
	return err
}

func (d *driver) releaseActions() error {
	_, err := d.do(http.MethodDelete, d.sess("/actions"), nil)
	return err
}

// screenshotPNG returns the current viewport as PNG bytes.
func (d *driver) screenshotPNG() ([]byte, error) {
	val, err := d.do(http.MethodGet, d.sess("/screenshot"), nil)
	if err != nil {
		return nil, err
	}
	var b64 string
	if err := json.Unmarshal(val, &b64); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(b64)
}

// logEntry is one browser console record (chromedriver's legacy log endpoint).
type logEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// consoleLog drains the browser console via the non-W3C log endpoint. The error
// is returned so callers can skip the check when the endpoint is unsupported.
func (d *driver) consoleLog() ([]logEntry, error) {
	val, err := d.do(http.MethodPost, d.sess("/log"), map[string]string{"type": "browser"})
	if err != nil {
		return nil, err
	}
	var entries []logEntry
	if err := json.Unmarshal(val, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
