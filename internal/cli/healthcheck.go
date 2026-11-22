package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/n-orlov/village-hall/internal/envvar"
	"github.com/n-orlov/village-hall/internal/server"
)

// HealthcheckUsage is the usage line of `vh healthcheck`.
const HealthcheckUsage = "usage: vh healthcheck [--url <url>]"

// HealthcheckTimeout bounds the whole probe: connect, request and status line.
const HealthcheckTimeout = 3 * time.Second

var healthcheckFlags = []flagSpec{{"--url", true}}

// healthcheckCmd runs `vh healthcheck`: it GETs the local server's /healthz
// (or --url) once and exits 0 on a 200, printing `ok healthz 200`, and 1 with
// one stderr line otherwise. It needs no key, no database and no shell, so a
// container built FROM scratch can run it as its health check.
func healthcheckCmd(args []string, stdout, stderr io.Writer, env Env) int {
	p, ok := parseArgs(args, healthcheckFlags)
	if !ok || len(p.pos) != 0 || len(p.values["--url"]) > 1 {
		return usageLine(stderr, HealthcheckUsage)
	}
	url := healthzURL(envvar.Listen.Get(env))
	if v := p.values["--url"]; len(v) == 1 {
		url = v[0]
	}
	if err := probe(url, HealthcheckTimeout); err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintln(stdout, "ok healthz 200")
	return ExitOK
}

// healthzURL is the /healthz URL of a server listening on addr (VH_LISTEN,
// default :8080): over 127.0.0.1 when addr names no host or a wildcard one.
func healthzURL(addr string) string {
	if addr == "" {
		addr = server.DefaultListen
	}
	host, port, _ := net.SplitHostPort(addr)
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// probe GETs url once, ignoring any proxy settings, and returns nil when it
// answers 200 within timeout.
func probe(url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered %d", url, resp.StatusCode)
	}
	return nil
}
