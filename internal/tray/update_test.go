package tray

import (
	"testing"

	"juhas/internal/update"
)

func TestUpdateInfo(t *testing.T) {
	a := &App{version: "v0.1.0", upd: update.Status{Latest: "v0.2.0", URL: "u"}}
	if u := a.updateInfo(); u.Latest != "v0.2.0" || u.Dev {
		t.Errorf("newer release: %+v", u)
	}
	a.upd.Latest = "v0.1.0"
	if u := a.updateInfo(); u.Latest != "" {
		t.Errorf("same version must not offer an update: %+v", u)
	}
	a.version, a.upd.Latest = "b835c40-dirty", "v9.0.0"
	if u := a.updateInfo(); !u.Dev || u.Latest != "" {
		t.Errorf("dev build is never replaced: %+v", u)
	}
}
