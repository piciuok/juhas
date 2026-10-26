package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/piciuok/juhas/internal/doctor"
)

// doctorOptions is extended by the review package hook (skill detection).
var doctorOptions = func(withClaude bool) doctor.Options { return doctor.Options{WithClaude: withClaude} }

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	withClaude := fs.Bool("with-ai", false, "also test a short claude and codex call (costs tokens)")
	fs.Parse(args)
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	rep := doctor.Run(context.Background(), cfg, doctorOptions(*withClaude))
	printReport(rep)
	if rep.Worst() == doctor.Fail {
		return fmt.Errorf("%d problems to fix", rep.Count(doctor.Fail))
	}
	return nil
}

func printReport(rep *doctor.Report) {
	section := ""
	for _, c := range rep.Checks {
		if c.Section != section {
			section = c.Section
			fmt.Printf("\n%s\n", section)
		}
		fmt.Printf("  %s %s\n", c.Level.Symbol(), c.Msg)
		if c.Hint != "" && c.Level != doctor.OK {
			fmt.Printf("      → %s\n", c.Hint)
		}
	}
	fmt.Printf("\n%d ok, %d warnings, %d errors\n", rep.Count(doctor.OK), rep.Count(doctor.Warn), rep.Count(doctor.Fail))
}
