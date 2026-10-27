package config

import (
	"fmt"
	"strings"
	"time"
)

// WorkingHours limit when Juhas checks and bothers me: outside them there
// are no automatic checks, questions, notifications or sparks.
type WorkingHours struct {
	Enabled bool     `json:"enabled"`
	From    string   `json:"from"` // "08:00"
	To      string   `json:"to"`   // "16:00"; earlier than From = over midnight
	Days    []string `json:"days"` // mon … sun; empty = mon–fri
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("%q: want HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (w WorkingHours) validate() error {
	if !w.Enabled {
		return nil
	}
	from, err := clock(w.From)
	if err != nil {
		return fmt.Errorf("working_hours.from %w", err)
	}
	to, err := clock(w.To)
	if err != nil {
		return fmt.Errorf("working_hours.to %w", err)
	}
	if from == to {
		return fmt.Errorf("working_hours: from and to are both %s", w.From)
	}
	for _, d := range w.Days {
		if dayIndex(d) < 0 {
			return fmt.Errorf("working_hours.days %q: want mon, tue, wed, thu, fri, sat or sun", d)
		}
	}
	return nil
}

func dayIndex(d string) int {
	d = strings.ToLower(strings.TrimSpace(d))
	for i, w := range weekdays {
		if len(d) >= 3 && strings.HasPrefix(d, w) {
			return i
		}
	}
	return -1
}

func (w WorkingHours) workday(wd time.Weekday) bool {
	if len(w.Days) == 0 {
		return wd >= time.Monday && wd <= time.Friday
	}
	for _, d := range w.Days {
		if dayIndex(d) == int(wd) {
			return true
		}
	}
	return false
}

// On reports whether t is within working hours (always, when disabled or
// misconfigured). A span over midnight belongs to the day it starts on.
func (w WorkingHours) On(t time.Time) bool {
	if !w.Enabled {
		return true
	}
	from, err1 := clock(w.From)
	to, err2 := clock(w.To)
	if err1 != nil || err2 != nil || from == to {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if from < to {
		return w.workday(t.Weekday()) && m >= from && m < to
	}
	if m >= from {
		return w.workday(t.Weekday())
	}
	return m < to && w.workday(t.AddDate(0, 0, -1).Weekday())
}

// Next is when the state at t changes (start or end of working hours),
// zero when it never does.
func (w WorkingHours) Next(t time.Time) time.Time {
	if !w.Enabled {
		return time.Time{}
	}
	now := w.On(t)
	c := t.Truncate(time.Minute)
	for i := 0; i < 8*24*60; i++ {
		c = c.Add(time.Minute)
		if w.On(c) != now {
			return c
		}
	}
	return time.Time{}
}

// Label is "08:00–16:00 Mon–Fri" for the menu.
func (w WorkingHours) Label() string {
	days := "Mon–Fri"
	if len(w.Days) > 0 {
		var ds []string
		for _, d := range w.Days {
			if i := dayIndex(d); i >= 0 {
				ds = append(ds, strings.ToUpper(weekdays[i][:1])+weekdays[i][1:])
			}
		}
		days = strings.Join(ds, " ")
	}
	return strings.TrimSpace(w.From) + "–" + strings.TrimSpace(w.To) + " " + days
}
