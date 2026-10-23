package tray

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderDecodes(t *testing.T) {
	states := map[string]IconState{
		"idle-template":   {Template: true},
		"pending-tmpl":    {Template: true, Queued: 3},
		"flash-tmpl":      {Template: true, Queued: 3, Urgent: 1, Flash: true},
		"running-tmpl":    {Template: true, Running: true},
		"problem-tmpl":    {Template: true, Problem: true},
		"linux-idle":      {Badge: true},
		"linux-pending":   {Badge: true, Queued: 7},
		"linux-many":      {Badge: true, Queued: 12},
		"linux-urgent-on": {Badge: true, Queued: 7, Urgent: 2, Flash: true},
		"zap-0":           {Template: true, Urgent: 1, Flash: true, Frame: 0},
		"zap-1":           {Template: true, Urgent: 1, Flash: true, Frame: 1},
		"zap-2":           {Template: true, Urgent: 1, Flash: true, Frame: 2},
		"zap-linux":       {Badge: true, Urgent: 3, Flash: true, Frame: 3},
		"ask-linux-0":     {Badge: true, Queued: 3, Decide: 2, Ask: true, Frame: 0},
		"ask-linux-1":     {Badge: true, Queued: 3, Decide: 2, Ask: true, Frame: 1},
	}
	dir := os.Getenv("ICON_DUMP_DIR") // set to eyeball the PNGs
	for name, s := range states {
		b := Render(s)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if img.Bounds().Dx() != iconPx {
			t.Errorf("%s: size %v", name, img.Bounds())
		}
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, name+".png"), b, 0o644)
		}
	}
}
