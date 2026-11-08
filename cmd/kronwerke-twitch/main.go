// kronwerke-twitch is the Twitch bot of the Kronwerke Minecraft server, with its
// dashboard.
//
//	kronwerke-twitch            run (configuration from the environment)
//	kronwerke-twitch version    print the version and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kronwerke/twitch/internal/app"
	"github.com/kronwerke/twitch/internal/update"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-version", "--version":
			fmt.Println(version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q. Run without arguments, or with \"version\".\n", os.Args[1])
			os.Exit(2)
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config()
	if err != nil {
		log.Error("configuration", "err", err)
		os.Exit(1)
	}
	a, err := app.New(cfg, log)
	if err != nil {
		log.Error("start", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("starting", "version", version)
	err = a.Run(ctx)
	switch {
	case errors.Is(err, app.ErrRestart):
		os.Exit(0) // systemd starts the unit again (Restart=always)
	case errors.Is(err, context.Canceled):
		log.Info("stopped")
	case err != nil:
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func config() (app.Config, error) {
	stateDir := env("TTV_STATE_DIR", "/srv/projects/kronwerke-twitch/state")
	cfg := app.Config{
		ClientID:     env("TTV_CLIENT_ID", ""),
		ClientSecret: env("TTV_CLIENT_SECRET", ""),
		PublicURL:    strings.TrimRight(env("TTV_PUBLIC_URL", "https://ttv.kronwerke.com"), "/"),
		Owner:        env("TTV_OWNER", "elchi_dev"),
		AdminToken:   env("TTV_ADMIN_TOKEN", ""),
		HTTPAddr:     env("TTV_HTTP_ADDR", "127.0.0.1:13040"),
		StatePath:    filepath.Join(stateDir, "state.json"),
		Version:      version,
		IRCAddr:      env("TTV_IRC_ADDR", "irc.chat.twitch.tv:6697"),
	}
	if cfg.AdminToken != "" && len(cfg.AdminToken) < 32 {
		return cfg, errors.New("TTV_ADMIN_TOKEN is shorter than 32 characters")
	}
	if repo := env("TTV_UPDATE_REPO", "kronwerke/twitch"); repo != "off" && version != "dev" {
		exe, err := os.Executable()
		if err != nil {
			return cfg, fmt.Errorf("find own binary: %w", err)
		}
		exe, _ = filepath.EvalSymlinks(exe)
		cfg.Updater = &update.Updater{
			Repo:      repo,
			API:       env("TTV_UPDATE_API", "https://api.github.com"),
			Binary:    exe,
			TrialFile: filepath.Join(stateDir, "update.trial"),
			SkipFile:  filepath.Join(stateDir, "update.skip"),
			Current:   version,
		}
	}
	return cfg, nil
}
