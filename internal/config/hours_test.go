package config

import (
	"testing"
	"time"
)

func TestWorkingHours(t *testing.T) {
	at := func(day, hm string) time.Time { // 2026-10-26 is a Monday
		d, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-"+day+" "+hm, time.Local)
		return d
	}
	w := WorkingHours{Enabled: true, From: "08:00", To: "16:00"}
	for _, c := range []struct {
		t  time.Time
		on bool
	}{
		{at("26", "07:59"), false}, {at("26", "08:00"), true}, {at("26", "15:59"), true}, {at("26", "16:00"), false},
		{at("31", "10:00"), false}, // Saturday
	} {
		if got := w.On(c.t); got != c.on {
			t.Errorf("On(%s) = %v", c.t.Format("Mon 15:04"), got)
		}
	}
	if n := w.Next(at("26", "17:00")); !n.Equal(at("27", "08:00")) {
		t.Errorf("next after Mon 17:00 = %s", n)
	}
	if n := w.Next(at("30", "17:00")); !n.Equal(at("02", "08:00").AddDate(0, 1, 0)) {
		t.Errorf("next after Fri 17:00 = %s, want Mon 08:00", n)
	}
	night := WorkingHours{Enabled: true, From: "22:00", To: "06:00", Days: []string{"fri"}}
	if !night.On(at("30", "23:00")) || !night.On(at("31", "05:00")) || night.On(at("31", "23:00")) {
		t.Error("over midnight: Fri 22:00 – Sat 06:00")
	}
	if !(WorkingHours{}).On(at("31", "03:00")) {
		t.Error("disabled = always on")
	}
	if err := (WorkingHours{Enabled: true, From: "8", To: "16:00"}).validate(); err == nil {
		t.Error("bad time must fail")
	}
	if err := (WorkingHours{Enabled: true, From: "08:00", To: "16:00", Days: []string{"monday", "xyz"}}).validate(); err == nil {
		t.Error("bad day must fail")
	}
	if l := w.Label(); l != "08:00–16:00 Mon–Fri" {
		t.Errorf("label %q", l)
	}
}
