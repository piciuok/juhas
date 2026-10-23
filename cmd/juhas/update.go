package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"juhas/internal/config"
	"juhas/internal/update"
)

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	pre := fs.Bool("pre", false, "also consider pre-releases (default: updates.prerelease)")
	force := fs.Bool("force", false, "install even over a development build")
	fs.Parse(args)
	prerelease := *pre
	if cfg, err := config.Load(config.DefaultPath()); err == nil {
		prerelease = prerelease || cfg.Updates.Prerelease
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := &update.Client{Version: version}
	rel, err := c.Latest(ctx, prerelease)
	if err != nil {
		return fmt.Errorf("checking %s: %w", update.Repo, err)
	}
	newer, err := update.Newer(version, rel.Tag)
	dev := errors.Is(err, update.ErrDevBuild)
	switch {
	case dev:
		fmt.Printf("This is a development build (%s); the latest release is %s.\n", version, rel.Tag)
		if *check {
			return nil
		}
		if !*force {
			return errors.New("not replacing a development build — run with --force to install the release")
		}
	case err != nil:
		return err
	case !newer:
		fmt.Printf("You are on the latest version (%s).\n", version)
		return nil
	default:
		fmt.Printf("%s is available (you have %s): %s\n", rel.Tag, version, rel.URL)
		if *check {
			return nil
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Printf("Downloading %s…\n", update.AssetName(rel.Tag, runtime.GOOS, runtime.GOARCH))
	if err := c.Install(ctx, rel, exe); err != nil {
		return err
	}
	fmt.Printf("Installed %s at %s (checksum verified).\n", rel.Tag, exe)
	fmt.Println("If the tray icon is running, quit it from the menu and start Juhas again (or log out and in).")
	return nil
}
