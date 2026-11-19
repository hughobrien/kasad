// Command kasad discovers TP-Link Kasa devices on the LAN, polls them in the
// background and serves their readings as Prometheus metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hughobrien/kasad/internal/exporter"
	"github.com/hughobrien/kasad/internal/kasa"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kasad:", err)
		os.Exit(1)
	}
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func run() error {
	var (
		listen    = flag.String("listen", "127.0.0.1:9881", "HTTP listen address")
		credsPath = flag.String("credentials", "", "file with the TP-Link account email (line 1) and password (line 2); optional, only newer devices need it")
		broadcast = flag.String("broadcast", "255.255.255.255", "comma-separated discovery targets (IPv4 broadcast addresses)")
		hosts     = flag.String("hosts", "", "comma-separated devices to poll even if discovery misses them")
		poll      = flag.Duration("poll-interval", 15*time.Second, "how often to poll each device")
		discover  = flag.Duration("discover-interval", time.Minute, "how often to broadcast for new devices")
		expire    = flag.Duration("expire", 10*time.Minute, "drop a discovered device after this long unseen")
		pollTO    = flag.Duration("poll-timeout", 5*time.Second, "timeout for one poll attempt")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return nil
	}

	cfg := exporter.Config{
		Broadcast:        list(*broadcast),
		Hosts:            list(*hosts),
		PollInterval:     *poll,
		DiscoverInterval: *discover,
		Expire:           *expire,
		PollTimeout:      *pollTO,
		DiscoverWait:     3 * time.Second,
	}
	if *credsPath != "" {
		c, err := readCreds(*credsPath)
		if err != nil {
			return err
		}
		cfg.Creds = c
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	e := exporter.New(cfg, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go e.Run(ctx)

	srv := &http.Server{Addr: *listen, Handler: e.Handler(version), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", *listen, "klap", cfg.Creds != nil)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func readCreds(path string) (*kasa.Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" || lines[1] == "" {
		return nil, fmt.Errorf("%s: want the email on line 1 and the password on line 2", path)
	}
	return &kasa.Credentials{Username: strings.TrimSpace(lines[0]), Password: strings.TrimRight(lines[1], "\r")}, nil
}
