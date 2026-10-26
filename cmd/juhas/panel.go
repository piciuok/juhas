package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/piciuok/juhas/internal/platform"
	"github.com/piciuok/juhas/internal/state"
	"github.com/piciuok/juhas/internal/tray"
)

// cmdPanel opens the running tray's panel page.
func cmdPanel(args []string) error {
	b, err := os.ReadFile(tray.WebURLFile(state.Open()))
	if err != nil {
		return errors.New("the Juhas tray icon is not running — start it: juhas tray")
	}
	url := strings.TrimSpace(string(b))
	if len(args) > 0 && args[0] == "--print" {
		fmt.Println(url)
		return nil
	}
	return platform.Open(url)
}
