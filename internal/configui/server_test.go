package configui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	saved := make(chan struct{}, 1)
	s, raw, err := Start(path, func() { saved <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, _ := url.Parse(raw)
	base := "http://" + u.Host
	token := u.Query().Get("t")

	call := func(method, p, tok string, body any) (*http.Response, map[string]any) {
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, base+p, rd)
		req.Header.Set("X-Token", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}

	if r, _ := call("GET", "/api/config", "wrong", nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("bad token: %d", r.StatusCode)
	}
	if r, _ := http.Get(base + "/"); r.StatusCode != http.StatusForbidden {
		t.Errorf("page without token: %d", r.StatusCode)
	}
	r, cfg := call("GET", "/api/config", token, nil)
	if r.StatusCode != 200 || cfg["exists"] != false {
		t.Fatalf("config: %d %v", r.StatusCode, cfg)
	}

	bad := map[string]any{"raw": map[string]any{"repos": map[string]any{"a/b": map[string]any{"mode": "all"}}}}
	if r, out := call("POST", "/api/save", token, bad); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(out["error"].(string), "mode") {
		t.Errorf("invalid save accepted: %d %v", r.StatusCode, out)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("invalid config was written")
	}

	good := map[string]any{"raw": map[string]any{"repos": map[string]any{"gl.example/a/b": map[string]any{"path": "~/x", "mode": "open"}}}}
	if r, out := call("POST", "/api/save", token, good); r.StatusCode != 200 {
		t.Fatalf("save: %d %v", r.StatusCode, out)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"mode": "open"`) {
		t.Errorf("saved file:\n%s", b)
	}
	<-saved
	if _, out := call("POST", "/api/save", token, good); out["ok"] != true {
		t.Fatal("second save")
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("no backup of the previous file")
	}

	// folder picker
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "b", ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "a"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".hidden"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644)
	_, ls := call("GET", "/api/ls?path="+url.QueryEscape(root), token, nil)
	dirs, _ := ls["dirs"].([]any)
	if len(dirs) != 2 || dirs[0].(map[string]any)["name"] != "a" || dirs[1].(map[string]any)["git"] != true {
		t.Errorf("ls: %v", ls)
	}
	if r, _ := call("GET", "/api/ls?path="+url.QueryEscape(filepath.Join(root, "nope")), token, nil); r.StatusCode == 200 {
		t.Error("missing dir listed")
	}
}
