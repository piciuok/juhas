package configui

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"

	"juhas/internal/doctor"
)

//go:embed doctor.html
var doctorHTML []byte

// DoctorURL is the diagnostics page.
func (s *Server) DoctorURL() string {
	return fmt.Sprintf("http://%s/doctor?t=%s", s.ln.Addr(), s.token)
}

func (s *Server) doctorPage(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r.URL.Query().Get("t")) {
		http.Error(w, "missing or invalid token — open Diagnostics from the tray icon menu", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = w.Write(doctorHTML)
}

type doctorResp struct {
	Report *doctor.Report    `json:"report"`
	Min    map[string]string `json:"min"`
	OS     string            `json:"os"`
}

// doctorGet returns the latest report, running the checks when there is none.
func (s *Server) doctorGet(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.Doctor == nil {
		return nil, errors.New("diagnostics not available in this session")
	}
	var rep *doctor.Report
	if s.LastDoctor != nil {
		rep = s.LastDoctor()
	}
	if rep == nil {
		rep = s.Doctor(false)
	}
	return doctorResp{Report: rep, Min: doctor.MinVersions, OS: runtime.GOOS}, nil
}

func (s *Server) doctorRun(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.Doctor == nil {
		return nil, errors.New("diagnostics not available in this session")
	}
	var req struct {
		WithAI bool `json:"with_ai"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		return nil, err
	}
	return doctorResp{Report: s.Doctor(req.WithAI), Min: doctor.MinVersions, OS: runtime.GOOS}, nil
}
