package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacy(t *testing.T) {
	stopLegacyFn = func() bool { return false }
	defer func() { stopLegacyFn = stopLegacy }()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	old := filepath.Join(home, ".local", "state", "kopas")
	_ = os.MkdirAll(filepath.Join(old, "jobs"), 0o755)
	_ = os.WriteFile(filepath.Join(old, "state.json"), []byte(`{"log":"`+old+`/jobs/a.log"}`), 0o600)
	_ = os.MkdirAll(filepath.Join(home, ".config", "kopas"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".config", "kopas", "config.json"), []byte("{}"), 0o600)
	_ = os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(home, ".local", "bin", "kopas"), []byte("x"), 0o755)

	done := MigrateLegacy()
	if len(done) < 2 {
		t.Fatalf("done = %v", done)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "juhas", "config.json")); err != nil {
		t.Error("config not moved")
	}
	b, _ := os.ReadFile(filepath.Join(home, ".local", "state", "juhas", "state.json"))
	if strings.Contains(string(b), "/kopas/") || !strings.Contains(string(b), "/juhas/jobs/a.log") {
		t.Errorf("state paths not rewritten: %s", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "kopas")); err == nil {
		t.Error("old binary left behind")
	}
	if again := MigrateLegacy(); len(again) != 0 {
		t.Errorf("second run did something: %v", again)
	}
}
