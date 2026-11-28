// Command kasad discovers TP-Link Kasa devices on the LAN, polls them in the
// background and serves their readings as Prometheus metrics.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hughobrien/kasad/internal/exporter"
	"github.com/hughobrien/kasad/internal/kasa"
	"github.com/hughobrien/kasad/internal/promwrite"
	"github.com/hughobrien/kasad/internal/web"
	"github.com/hughobrien/kasad/internal/web/templates"
	"github.com/hughobrien/kasad/internal/web/ui"
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

// check rejects settings that would panic or do nothing useful: a stray
// argument (probably a flag typed without its dashes) or a duration that is
// not positive (time.NewTicker panics on one).
func check(cfg exporter.Config, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unexpected argument %q (flags start with -)", args[0])
	}
	for _, d := range []struct {
		flag string
		v    time.Duration
	}{
		{"poll-interval", cfg.PollInterval},
		{"discover-interval", cfg.DiscoverInterval},
		{"expire", cfg.Expire},
		{"poll-timeout", cfg.PollTimeout},
	} {
		if d.v <= 0 {
			return fmt.Errorf("-%s must be positive, got %v", d.flag, d.v)
		}
	}
	return nil
}

func run() error {
	var targets promwrite.Targets
	flag.Var(&targets, "push", "name=url of a remote write receiver (VictoriaMetrics, or Prometheus with its receiver on) to push every reading to; repeat for more. /metrics serves the readings either way, labelled pushed_<name>=\"true\" for the receiver's own scrape to drop")
	var (
		listen    = flag.String("listen", "127.0.0.1:9881", "HTTP listen address")
		credsPath = flag.String("credentials", "", "file with the TP-Link account email (line 1) and password (line 2); optional, only newer devices need it")
		broadcast = flag.String("broadcast", "255.255.255.255", "comma-separated discovery targets (IPv4 broadcast addresses)")
		hosts     = flag.String("hosts", "", "comma-separated IP addresses of devices to poll even if discovery misses them (not DNS names: one that discovery also finds by address would show twice)")
		allowed   = flag.String("allowed-hosts", "", "comma-separated Host names the page may be reached by, besides IP addresses, localhost and this machine's hostname (a reverse proxy's name); /metrics and /healthz take any Host")
		poll      = flag.Duration("poll-interval", 15*time.Second, "how often to poll each device")
		discover  = flag.Duration("discover-interval", 15*time.Minute, "how often to broadcast for new devices (a device's successful polls keep it from expiring)")
		job       = flag.String("job", "kasad", "job label on pushed series")
		expire    = flag.Duration("expire", 10*time.Minute, "drop a discovered device after this long unseen, checked at each discovery sweep (devices in --hosts are never dropped)")
		pollTO    = flag.Duration("poll-timeout", 5*time.Second, "timeout for one poll attempt")
		metaPath  = flag.String("metadata", "", "JSON file naming rooms and devices plugged into other devices (see README); optional")
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
		Job:              *job,
	}
	if err := check(cfg, flag.Args()); err != nil {
		return err
	}
	if *credsPath != "" {
		c, err := readCreds(*credsPath)
		if err != nil {
			return err
		}
		cfg.Creds = c
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	e := exporter.New(cfg, promwrite.NewPushers(targets, nil), log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runDone := make(chan struct{})
	go func() { defer close(runDone); e.Run(ctx) }()

	var meta *ui.Meta
	if *metaPath != "" {
		m, err := ui.LoadMeta(*metaPath)
		if err != nil {
			return err
		}
		meta = m
		// Devices take a few discovery rounds to appear; after that, a name in
		// the file that matches nothing is a rename or a typo worth saying so.
		metaTimer := time.AfterFunc(2*time.Minute, func() {
			if names := m.Unmatched(e.Devices()); len(names) > 0 {
				log.Warn("metadata names match no device", "names", names)
			}
		})
		defer metaTimer.Stop()
	}

	templates.BuildID = buildID()
	// The machine's own hostname (short and as reported) is always allowed, so
	// http://myserver:9881 works without configuration.
	allowedHosts := list(*allowed)
	if h, err := os.Hostname(); err == nil {
		allowedHosts = append(allowedHosts, h, strings.SplitN(h, ".", 2)[0])
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           web.Handler(e, e.Handler(version), meta, allowedHosts),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	shutDone := make(chan struct{})
	go func() {
		defer close(shutDone)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("listening", "addr", *listen, "klap", cfg.Creds != nil)
	err := srv.ListenAndServe()
	// Stop, then wait for the HTTP shutdown and for Run's last flush to the
	// push targets; each is bounded (3 s and 2 s) and they run side by side.
	stop()
	<-shutDone
	<-runDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
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
	if len(lines) < 2 {
		return nil, fmt.Errorf("%s: want the email on line 1 and the password on line 2", path)
	}
	user := strings.TrimSpace(lines[0])
	pass := strings.TrimRight(lines[1], "\r")
	if user == "" || pass == "" {
		return nil, fmt.Errorf("%s: want the email on line 1 and the password on line 2", path)
	}
	return &kasa.Credentials{Username: user, Password: pass}, nil
}

// buildID hashes the running binary, which changes on every deploy, so an open
// browser tab notices a new daemon and reloads.
func buildID() string {
	if exe, err := os.Executable(); err == nil {
		if f, err := os.Open(exe); err == nil {
			defer f.Close()
			h := sha256.New()
			if _, err := io.Copy(h, f); err == nil {
				return hex.EncodeToString(h.Sum(nil))[:12]
			}
		}
	}
	return "dev"
}
