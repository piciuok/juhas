package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"juhas/internal/config"
	"juhas/internal/configui"
	"juhas/internal/doctor"
	"juhas/internal/platform"
	"juhas/internal/review"
)

func init() {
	configui.Detect = func(path string, r config.Review) any { return review.Resolve(path, r) }
}

// configUI serves the settings page until it is closed or idles out.
func configUI(args []string) error {
	fs := flag.NewFlagSet("config ui", flag.ExitOnError)
	noOpen := fs.Bool("no-open", false, "only print the address, do not open the browser")
	fs.Parse(args)
	_, _ = loadConfig() // registers the accounts for the review page; the page itself reports a broken config
	s, url, err := configui.Start(config.DefaultPath(), nil)
	if err != nil {
		return err
	}
	s.Doctor = func(withAI bool) *doctor.Report {
		cfg, err := loadConfig()
		if err != nil {
			return &doctor.Report{At: time.Now(), Checks: []doctor.Check{{Level: doctor.Fail, Section: "Config", Msg: err.Error()}}}
		}
		return doctor.Run(context.Background(), cfg, doctorOptions(withAI))
	}
	fmt.Println("Settings:", url)
	fmt.Println("(Close it in the browser or press Ctrl+C)")
	if !*noOpen {
		_ = platform.Open(url)
	}
	<-s.Done()
	return nil
}
