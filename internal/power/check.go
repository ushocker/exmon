package power

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"exmon/internal/winnet"
)

// Checker reports whether the network is ready, with a short description either way.
type Checker func(ctx context.Context) (ready bool, detail string)

// NetworkCheck is the real readiness check: some adapter that is up has a default
// route (Wi-Fi or wired), and, if a test URL is set, fetching it returns the expected
// text. A captive portal ("sign in to the hotel Wi-Fi") answers with its own page,
// so the text check fails there, as it should.
func NetworkCheck(cfg Config) Checker {
	return func(ctx context.Context) (bool, string) {
		ads, err := winnet.Adapters()
		if err != nil {
			return false, "adapters: " + err.Error()
		}
		routes, err := winnet.DefaultRoutes()
		if err != nil {
			return false, "routes: " + err.Error()
		}
		_, ad, ok := winnet.BestDefaultRoute(routes, ads)
		if !ok {
			return false, "no network with a default route"
		}
		if cfg.TestURL == "" {
			return true, "default route via " + ad.Name
		}
		if err := connectTest(ctx, cfg.TestURL, cfg.Expect); err != nil {
			return false, fmt.Sprintf("via %s, connection test failed: %v", ad.Name, err)
		}
		return true, fmt.Sprintf("via %s, connection test passed", ad.Name)
	}
}

// connectTest fetches url and checks the reply contains expect.
func connectTest(ctx context.Context, url, expect string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if expect != "" && !strings.Contains(string(body), expect) {
		return fmt.Errorf("unexpected reply (captive portal or proxy?)")
	}
	return nil
}
