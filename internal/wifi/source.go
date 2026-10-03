// Package wifi is the Wi-Fi monitor: it logs WLAN notifications as they happen and
// polls each adapter on a timer, logging a POLL line only when something changed.
package wifi

import (
	"context"
	"fmt"
	"time"

	"exmon/internal/event"
	"exmon/internal/wlan"
)

const (
	maxConsecutivePollFailures = 3
	firstRetryDelay            = 5 * time.Second
	maxRetryDelay              = 60 * time.Second
)

// Source implements source.Source for Wi-Fi.
type Source struct {
	cfg Config
}

// New creates the Wi-Fi source.
func New(cfg Config) *Source { return &Source{cfg: cfg} }

// Name implements source.Source.
func (s *Source) Name() string { return "wifi" }

// Run implements source.Source. Notifications and polls are both handled in the one
// select loop below, so nothing here runs concurrently and no mutex is needed.
func (s *Source) Run(ctx context.Context, log event.Logger) error {
	notes := make(chan wlan.Notification, 256) // buffered so the Windows callback never waits
	p := newPoller(s.cfg)
	log.DeclareMetrics(metricDirections)

	client, err := s.open(ctx, log, notes, p)
	if err != nil {
		return nil // ctx was cancelled while we were still trying to start
	}
	// The deferred func reads client when Run returns, so it closes whichever client
	// is current at that point, even after a reopen.
	defer func() { s.close(client, log) }()

	if s.cfg.DebugPoll {
		log.Log("MONITOR", "wifi: logging every poll (debug)")
	} else {
		heartbeat := "off"
		if s.cfg.HeartbeatSeconds > 0 {
			heartbeat = fmt.Sprintf("every %ds", s.cfg.HeartbeatSeconds)
		}
		log.Log("MONITOR", fmt.Sprintf("wifi: polls logged on change: RSSI >= %ddBm, rate >= %d%%, retry/FCS >= %dpts; heartbeat %s",
			s.cfg.RSSIThreshold, s.cfg.RateChangePercent, s.cfg.ErrorPctThreshold, heartbeat))
	}

	ticker := time.NewTicker(time.Duration(max(1, s.cfg.PollSeconds)) * time.Second)
	defer ticker.Stop()

	failures := 0
	doPoll := func() error {
		err := p.poll(client, log)
		if err == nil {
			failures = 0
			return nil
		}
		failures++
		log.Log("ERROR", "poll: "+err.Error())

		// A stale handle (WlanSvc restarted, adapter reset) never recovers on its own.
		if !wlan.IsStale(err) && failures < maxConsecutivePollFailures {
			return nil
		}
		log.Log("MONITOR", "re-opening wlanapi handle")
		s.close(client, log)
		client, err = s.open(ctx, log, notes, p)
		failures = 0
		return err // non-nil only if ctx was cancelled
	}

	if doPoll() != nil { // poll once right away, then on every tick
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			if d := wlan.Dropped(); d > 0 {
				log.Log("MONITOR", fmt.Sprintf("wifi: %d notifications dropped (queue full)", d))
			}
			return nil
		case n := <-notes:
			if kind, detail, ok := describe(n); ok {
				log.Log(kind, detail)
			}
		case <-ticker.C:
			if doPoll() != nil {
				return nil
			}
		}
	}
}

// open opens a wlanapi session and subscribes to notifications. If that fails
// (WlanSvc not running yet, for example) it retries, waiting 5s, 10s, 20s ... up to
// 60s between attempts. It returns an error only when ctx is cancelled.
func (s *Source) open(ctx context.Context, log event.Logger, notes chan<- wlan.Notification, p *poller) (*wlan.Client, error) {
	delay := firstRetryDelay
	for {
		c, err := wlan.Open()
		if err == nil {
			if err = c.Subscribe(notes); err == nil {
				p.reset()
				log.Log("MONITOR", "wifi: started")
				return c, nil
			}
			c.Close()
		}
		log.Log("ERROR", fmt.Sprintf("start: %v (retrying in %v)", err, delay))

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetryDelay)
	}
}

func (s *Source) close(c *wlan.Client, log event.Logger) {
	if c == nil {
		return
	}
	c.Close()
	log.Log("MONITOR", "wifi: stopped")
}
