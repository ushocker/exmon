package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"
)

// httpPhases are the timings of one request, measured from the request start.
// httptrace calls our hooks as each phase happens, so these are real numbers,
// not estimates.
type httpPhases struct {
	dns, connect, tls, ttfb, total time.Duration
}

// checkHTTP fetches one URL on a fresh connection and times each phase.
func checkHTTP(ctx context.Context, t HTTPTest, timeout time.Duration) result {
	return checkHTTPWith(ctx, t, timeout, &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true,
		DialContext: guardedDialContext(&net.Dialer{Timeout: timeout})})
}

// checkHTTPWith is checkHTTP with a given transport (tests pass one that trusts
// their self-signed certificate).
func checkHTTPWith(ctx context.Context, t HTTPTest, timeout time.Duration, transport *http.Transport) result {
	r := result{name: t.Name, kind: "PROBE/http", metrics: map[string]float64{}}

	var ph httpPhases
	var dnsStart, connStart, tlsStart time.Time
	start := time.Now()
	// A ClientTrace is a set of callbacks the HTTP client invokes as it works.
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { ph.dns = time.Since(dnsStart) },
		ConnectStart:         func(string, string) { connStart = time.Now() },
		ConnectDone:          func(string, string, error) { ph.connect = time.Since(connStart) },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { ph.tls = time.Since(tlsStart) },
		GotFirstResponseByte: func() { ph.ttfb = time.Since(start) },
	}

	// Keep-alives off: every test opens a fresh connection, so DNS, TCP and TLS are
	// measured every time. Clone so the caller's transport is not changed.
	tr := transport.Clone()
	tr.DisableKeepAlives = true
	// A hand-made Transport only speaks HTTP/1.1 unless asked; many servers (Microsoft
	// 365 included) pick HTTP/2, like a browser would, so allow it.
	tr.ForceAttemptHTTP2 = true
	client := &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: checkRedirect}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, t.URL, nil)
	if err != nil {
		r.detail = fmt.Sprintf("[%s] bad url: %v", t.Name, err)
		return r
	}
	req.Header.Set("User-Agent", "exmon-probe")

	resp, err := client.Do(req)
	if err != nil {
		ph.total = time.Since(start)
		r.metrics["http_ok"] = 0
		r.detail = fmt.Sprintf("[%s] %s FAILED after %s: %v", t.Name, t.URL, ms(ph.total), err)
		return r
	}
	n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 10<<20)) // read up to 10 MB
	resp.Body.Close()
	ph.total = time.Since(start)

	r.up = resp.StatusCode < 500 // a 4xx still proves the service answered
	r.metrics["http_ok"] = b2f(resp.StatusCode < 400)
	r.metrics["http_total_ms"] = msf(ph.total)
	r.metrics["http_ttfb_ms"] = msf(ph.ttfb)
	if ph.tls > 0 {
		r.metrics["http_tls_ms"] = msf(ph.tls)
	}

	extra := ""
	if final := resp.Request.URL.String(); final != t.URL {
		extra += " redirected=" + final
	}
	if cdn := cdnName(resp.Header); cdn != "" {
		extra += " cdn=" + cdn
	}
	if srv := resp.Header.Get("Server"); srv != "" {
		extra += fmt.Sprintf(" server=%q", srv)
	}
	r.detail = fmt.Sprintf("[%s] %d dns=%s connect=%s tls=%s ttfb=%s total=%s bytes=%d%s",
		t.Name, resp.StatusCode, ms(ph.dns), ms(ph.connect), ms(ph.tls), ms(ph.ttfb), ms(ph.total), n, extra)
	return r
}

// cdnName recognises a CDN from its response headers.
func cdnName(h http.Header) string {
	switch {
	case h.Get("X-Azure-Ref") != "":
		return "AzureFrontDoor"
	case h.Get("X-MSEdge-Ref") != "":
		return "AzureCDN"
	case h.Get("CF-RAY") != "":
		return "Cloudflare"
	case h.Get("X-Amz-Cf-Id") != "":
		return "CloudFront"
	}
	return ""
}
