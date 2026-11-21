package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/demo"
	"github.com/YunusEmreAlps/geko/internal/ownership"
	"github.com/YunusEmreAlps/geko/internal/storage"
	"github.com/YunusEmreAlps/geko/internal/storage/sqlite"
)

const (
	// A public demo answers each client address at most this often.
	publicRequestRate  = 10
	publicRequestBurst = 40
	// liveInterval adds a demo trace this often, so live views keep moving.
	liveInterval = 2 * time.Second
)

func demoCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(out)
	listen := flags.String("listen", "127.0.0.1:0", "address to serve the demo on; any address other than loopback serves it read-only to anyone")
	noOpen := flags.Bool("no-open", false, "do not open the dashboard in a browser")
	readOnly := flags.Bool("read-only", false, "refuse exports and changes")
	proxies := flags.String("trusted-proxy", "", "comma-separated addresses or CIDR prefixes of a reverse proxy whose X-Forwarded-For names the client")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: geko demo [--listen ADDRESS] [--no-open] [--read-only] [--trusted-proxy CIDR,...]")
	}
	trusted, err := parsePrefixes(*proxies)
	if err != nil {
		return fmt.Errorf("--trusted-proxy: %w", err)
	}
	address, err := netip.ParseAddrPort(*listen)
	if err != nil || address.Addr().Zone() != "" {
		return fmt.Errorf("--listen %q must be an IP address and port, such as 127.0.0.1:4318", *listen)
	}
	public := !address.Addr().IsLoopback()
	directory, err := os.MkdirTemp("", "geko-demo-")
	if err != nil {
		return fmt.Errorf("create the demo database directory: %w", err)
	}
	defer os.RemoveAll(directory)
	demoSetting := config.SettingSource{Kind: "demo"}
	cfg := config.Config{
		ListenAddr: address.String(), DBPath: filepath.Join(directory, "geko.db"),
		RetentionDays: config.DefaultRetentionDays, MaxDBMiB: 128, IngestSlots: config.DefaultIngestConcurrency,
		OnFull: storage.EvictOldest, Redaction: config.DefaultRedaction, ReadOnly: *readOnly || public,
		Source: config.Source{
			ListenAddr: demoSetting, DBPath: demoSetting, RetentionDays: demoSetting, MaxDBMiB: demoSetting,
			OnFull: demoSetting, Redaction: demoSetting, IngestSlots: demoSetting,
			DashboardAddr: demoSetting, GRPCAddr: demoSetting, PublicURL: demoSetting,
		},
	}
	if cfg.Owners, err = ownership.Parse(demo.Owners); err != nil {
		return err
	}
	var dataset *demo.Dataset
	options := serveOptions{
		open: !*noOpen && !public, public: public, trustedProxies: trusted,
		prepare: func(ctx context.Context, db *sqlite.Store) error {
			seeded, err := demo.Seed(ctx, db, time.Now())
			if err != nil {
				return err
			}
			dataset = seeded
			fmt.Fprintf(out, "Geko demo: %d synthetic traces from %d services in %d applications over the last %s.\n",
				dataset.Traces, dataset.Services, dataset.Applications, demo.History)
			fmt.Fprintf(out, "web-frontend %s replaced %s %s ago; compare them on Version regressions.\n",
				demo.Candidate, demo.Baseline, demo.Deployed)
			fmt.Fprintln(out, "The demo database is temporary and is deleted when you stop Geko with Ctrl+C.")
			if public {
				fmt.Fprintln(out, "Serving read-only to anyone who reaches this address, over plain HTTP. Put a TLS reverse proxy in front of it.")
			}
			return nil
		},
		background: func(ctx context.Context, db *sqlite.Store) { dataset.Run(ctx, db, liveInterval) },
	}
	return serve(ctx, cfg, out, options)
}

// startBrowser opens url without waiting for the browser to exit.
var startBrowser = func(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.Command("open", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

// openDashboard opens the dashboard served at address, which may be an
// unspecified address such as 0.0.0.0:4318.
func openDashboard(out io.Writer, address string) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/"
	if err := startBrowser(url); err != nil {
		fmt.Fprintf(out, "Open %s in a browser (could not start one: %v)\n", url, err)
	}
}

type clientAllowance struct {
	tokens float64
	seen   time.Time
}

func parsePrefixes(list string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, value := range strings.Split(list, ",") {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil {
				return nil, fmt.Errorf("%q is not an IP address or CIDR prefix", value)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// clientAddress is the request's peer, or the address a trusted proxy last
// appended to X-Forwarded-For.
func clientAddress(r *http.Request, trusted []netip.Prefix) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	address, err := netip.ParseAddr(peer)
	if err != nil {
		return peer
	}
	for _, prefix := range trusted {
		if prefix.Contains(address.Unmap()) {
			forwarded := r.Header.Values("X-Forwarded-For")
			if len(forwarded) == 0 {
				return peer
			}
			hops := strings.Split(forwarded[len(forwarded)-1], ",")
			if client := strings.TrimSpace(hops[len(hops)-1]); client != "" {
				return client
			}
			return peer
		}
	}
	return peer
}

// limitPerClient answers HTTP 429 once a client address has used its burst
// and exceeds rate requests a second. Without trusted proxies, every request
// through a reverse proxy shares its address and the limit applies to all.
func limitPerClient(next http.Handler, rate float64, burst int, trusted []netip.Prefix) http.Handler {
	var mu sync.Mutex
	clients := make(map[string]*clientAllowance)
	var swept time.Time
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := clientAddress(r, trusted)
		now := time.Now()
		mu.Lock()
		if now.Sub(swept) > time.Minute {
			for address, allowance := range clients {
				if now.Sub(allowance.seen) > time.Minute {
					delete(clients, address)
				}
			}
			swept = now
		}
		allowance, ok := clients[client]
		if !ok {
			allowance = &clientAllowance{tokens: float64(burst), seen: now}
			clients[client] = allowance
		}
		allowance.tokens = min(float64(burst), allowance.tokens+now.Sub(allowance.seen).Seconds()*rate)
		allowance.seen = now
		admitted := allowance.tokens >= 1
		if admitted {
			allowance.tokens--
		}
		mu.Unlock()
		if !admitted {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Too many requests to this demo; try again in a moment", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
