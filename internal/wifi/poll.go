package wifi

import (
	"fmt"
	"strings"
	"time"

	"exmon/internal/event"
	"exmon/internal/wlan"
)

// ifaceSample is one interface's state at one poll.
type ifaceSample struct {
	Key  string // interface GUID
	Desc string // interface description
	Sig  string // fields where any change at all is worth logging

	Connected     bool
	RSSI          int // dBm; only meaningful when HasRSSI
	HasRSSI       bool
	Rx, Tx        int    // kbps
	SignalQuality uint32 // 0-100

	Text      string // this interface's part of the POLL line
	Neighbors string // NEIGHBORS detail, or ""

	Stats          *wlan.Counters // cumulative counters, or nil if unsupported
	RetryPct       float64        // retries / tx frames since the last logged POLL
	FCSPct         float64        // FCS errors / (rx frames + FCS errors) over the same interval
	HandshakeFails int64          // four-way handshake failures over the same interval
	StatsText      string         // STATS line, or "" (first sample / unsupported)

	// A percentage only means something when enough frames went by and the driver's
	// counters are believable. These say whether RetryPct / FCSPct can be trusted.
	RetryValid bool
	FCSValid   bool

	// The retry/FCS percentages from the last logged sample that was valid. Change
	// detection compares against these, so a logged-but-noisy sample never becomes
	// the yardstick. Carried forward from sample to sample when logging.
	RefRetryPct, RefFCSPct float64
	HasRefRetry, HasRefFCS bool
}

// Metric names reported on every poll, and which direction is bad for each.
var metricDirections = map[string]event.Direction{
	"rssi_dbm":           event.LowIsBad,
	"signal_quality":     event.LowIsBad,
	"rx_kbps":            event.LowIsBad,
	"tx_kbps":            event.LowIsBad,
	"retry_pct":          event.HighIsBad,
	"fcs_pct":            event.HighIsBad,
	"handshake_fail":     event.HighIsBad,
	"recent_disconnects": event.HighIsBad,
}

// poller remembers the last logged sample so it can log only on change.
type poller struct {
	cfg         Config
	last        map[string]ifaceSample   // keyed by interface GUID
	prevStats   map[string]wlan.Counters // counters at the previous poll, for per-poll metrics
	statsWarned bool

	// Adapters whose driver reported impossible counters (retries > frames sent).
	// Their retry/FCS numbers are never used again. Kept across handle reopens,
	// because the driver doesn't change.
	inconsistent map[string]bool

	// Times of ACM/disconnected notifications per interface, for recent_disconnects.
	// Kept across handle reopens: a WlanSvc restart doesn't undo a disconnect.
	disconnects map[string][]time.Time

	lastLogged time.Time        // when the last POLL line was written, for the heartbeat
	now        func() time.Time // the clock; tests replace it
}

func newPoller(cfg Config) *poller {
	return &poller{
		cfg:          cfg,
		last:         map[string]ifaceSample{},
		prevStats:    map[string]wlan.Counters{},
		inconsistent: map[string]bool{},
		disconnects:  map[string][]time.Time{},
		now:          time.Now, // a function value: p.now() calls time.Now()
	}
}

// reset forgets the baseline, e.g. after the handle is reopened.
func (p *poller) reset() {
	p.last = map[string]ifaceSample{}
	p.prevStats = map[string]wlan.Counters{}
	p.statsWarned = false
	p.lastLogged = time.Time{}
}

// noteDisconnect records an ACM/disconnected notification for an interface.
func (p *poller) noteDisconnect(key string) {
	p.disconnects[key] = append(p.disconnects[key], p.now())
}

// recentDisconnects counts an interface's disconnects within the last
// DisconnectWindowMinutes and forgets older ones.
func (p *poller) recentDisconnects(key string) int {
	cutoff := p.now().Add(-time.Duration(p.cfg.DisconnectWindowMinutes) * time.Minute)
	times := p.disconnects[key]
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	p.disconnects[key] = times[i:]
	return len(times) - i
}

// heartbeatDue reports whether it has been HeartbeatSeconds since the last POLL line.
// Polls don't land exactly on the second, so half a poll interval of slack is
// allowed: with 5s polls and a 15s heartbeat, the third quiet poll logs.
func (p *poller) heartbeatDue(now time.Time) bool {
	if p.cfg.HeartbeatSeconds <= 0 {
		return false
	}
	slack := time.Duration(p.cfg.PollSeconds) * time.Second / 2
	return now.Sub(p.lastLogged)+slack >= time.Duration(p.cfg.HeartbeatSeconds)*time.Second
}

// heartbeatBaseline is what a heartbeat line remembers. Only the frame counters move
// forward (so the next STATS line covers the time since this one); the RSSI, rate
// and retry references stay put, so a slow drift still adds up to a change.
func heartbeatBaseline(s, prev ifaceSample) ifaceSample {
	if prev.Key == "" { // no earlier line (can't really happen: no change implies one exists)
		return withReference(s, prev)
	}
	prev.Stats = s.Stats
	return prev
}

// checkConsistency compares each adapter's counters with the previous poll. In
// 802.11, the retry counter counts *frames* that needed a retry (not individual
// retransmissions), so it can never grow faster than frames sent. If it does, the
// driver's counters can't be trusted: warn once and stop using retry/FCS for that adapter.
func (p *poller) checkConsistency(samples []ifaceSample, log event.Logger) {
	for _, s := range samples {
		if s.Stats == nil || p.inconsistent[s.Key] {
			continue
		}
		prev, ok := p.prevStats[s.Key]
		if !ok {
			continue
		}
		if d := diffStats(*s.Stats, prev); d.retry > d.tx {
			p.inconsistent[s.Key] = true
			log.Log("STATS", fmt.Sprintf("[%s] driver counters look inconsistent (retry=%d > tx=%d in one poll); retry/FCS change detection disabled",
				s.Desc, d.retry, d.tx))
		}
	}
}

// reportMetrics sends one METRICS event per connected interface, on every poll.
// The frame-counter values cover just the time since the previous poll.
func (p *poller) reportMetrics(samples []ifaceSample, log event.Logger) {
	for _, s := range samples {
		if !s.Connected {
			continue
		}
		m := map[string]float64{
			"rx_kbps":        float64(s.Rx),
			"tx_kbps":        float64(s.Tx),
			"signal_quality": float64(s.SignalQuality),
		}
		if s.HasRSSI {
			m["rssi_dbm"] = float64(s.RSSI)
		}
		if s.Stats != nil {
			if prev, ok := p.prevStats[s.Key]; ok {
				d := diffStats(*s.Stats, prev)
				m["handshake_fail"] = float64(d.hs)
				// Percentages only when they mean something (see statDelta.retryValid).
				if d.retryValid(p.cfg.MinStatsFrames, p.inconsistent[s.Key]) {
					m["retry_pct"] = d.retryPct()
				}
				if d.fcsValid(p.cfg.MinStatsFrames, p.inconsistent[s.Key]) {
					m["fcs_pct"] = d.fcsPct()
				}
			}
			p.prevStats[s.Key] = *s.Stats
		}
		m["recent_disconnects"] = float64(p.recentDisconnects(s.Key))
		log.Metrics(s.Desc, m)
	}
}

// poll samples every interface and logs a POLL line if something changed.
func (p *poller) poll(c *wlan.Client, log event.Logger) error {
	ifaces, err := c.Interfaces()
	if err != nil {
		return err
	}
	samples := make([]ifaceSample, 0, len(ifaces))
	for _, iface := range ifaces {
		samples = append(samples, p.sample(c, log, iface))
	}

	p.checkConsistency(samples, log) // must run before reportMetrics updates prevStats
	p.reportMetrics(samples, log)

	computeStatDeltas(samples, p.last, p.cfg.MinStatsFrames, p.inconsistent)

	now := p.now()
	heartbeat := false
	switch {
	case p.cfg.DebugPoll, hasChanged(samples, p.last, p.cfg):
	case p.heartbeatDue(now):
		heartbeat = true
	default:
		return nil // nothing changed and the heartbeat isn't due
	}
	p.lastLogged = now

	// Baselines advance only when a line is written, so slow drift still trips a threshold.
	old := p.last
	p.last = make(map[string]ifaceSample, len(samples))
	parts := make([]string, 0, len(samples))
	for _, s := range samples {
		if heartbeat {
			p.last[s.Key] = heartbeatBaseline(s, old[s.Key])
		} else {
			p.last[s.Key] = withReference(s, old[s.Key])
		}
		parts = append(parts, s.Text)
	}
	line := strings.Join(parts, "; ")
	if heartbeat {
		line += " [heartbeat]"
	}
	log.Log("POLL", line)
	for _, s := range samples {
		if s.StatsText != "" {
			log.Log("STATS", s.StatsText)
		}
		if s.Neighbors != "" {
			log.Log("NEIGHBORS", s.Neighbors)
		}
	}
	return nil
}

func (p *poller) sample(c *wlan.Client, log event.Logger, iface wlan.Interface) ifaceSample {
	s := ifaceSample{Key: iface.GUID.String(), Desc: iface.Description}

	if iface.State != wlan.InterfaceStateConnected {
		state := wlan.IfStateName(iface.State)
		s.Sig = "state=" + state
		s.Text = fmt.Sprintf("[%s] state=%s", s.Desc, state)
		return s
	}

	conn, err := c.CurrentConnection(iface.GUID)
	if err != nil {
		s.Sig = "queryfail=" + err.Error()
		s.Text = fmt.Sprintf("[%s] query failed: %v", s.Desc, err)
		return s
	}
	s.Connected = true
	s.Rx, s.Tx = int(conn.RxRate), int(conn.TxRate)
	s.SignalQuality = conn.SignalQuality
	phy := wlan.PhyTypeName(conn.PhyType)

	if st, err := c.Statistics(iface.GUID); err == nil {
		s.Stats = statsPtr(st)
	} else if !p.statsWarned {
		p.statsWarned = true
		log.Log("STATS", fmt.Sprintf("driver does not expose 802.11 frame statistics (%v); STATS lines disabled", err))
	}

	// The driver's RSSI comes from the BSS list, not the connection attributes.
	bssList, _ := c.BssList(iface.GUID) // on error we simply have no RSSI this time
	var cur *wlan.BSS
	var neighbors []string
	for i := range bssList {
		b := &bssList[i]
		switch {
		case b.BSSID == conn.BSSID:
			cur = b
		case p.cfg.Neighbors && b.SSID == conn.SSID:
			ch, _ := wlan.FreqToChannel(b.FreqKHz)
			neighbors = append(neighbors, fmt.Sprintf("%s ch%d %ddBm %s", b.BSSID, ch, b.RSSI, bssExtra(b)))
		}
	}

	sec := fmt.Sprintf("auth=%s cipher=%s", wlan.AuthName(conn.AuthAlgorithm), wlan.CipherName(conn.Cipher))
	if cur != nil {
		ch, band := wlan.FreqToChannel(cur.FreqKHz)
		s.RSSI, s.HasRSSI = cur.RSSI, true
		s.Sig = fmt.Sprintf("connected|%s|%s|%d|%s|%d|%s", conn.SSID, conn.BSSID, ch, band, conn.PhyType, sec)
		s.Text = fmt.Sprintf("[%s] ssid=%q bssid=%s rssi=%ddBm ch=%d (%s) phy=%s %s rx=%dkbps tx=%dkbps %s",
			s.Desc, conn.SSID, conn.BSSID, cur.RSSI, ch, band, phy, sec, s.Rx, s.Tx, bssExtra(cur))
	} else {
		s.Sig = fmt.Sprintf("connected-norssi|%s|%s|%d|%s", conn.SSID, conn.BSSID, conn.PhyType, sec)
		s.Text = fmt.Sprintf("[%s] ssid=%q bssid=%s rssi=n/a (bssid not in scan cache) phy=%s %s rx=%dkbps tx=%dkbps",
			s.Desc, conn.SSID, conn.BSSID, phy, sec, s.Rx, s.Tx)
	}
	if len(neighbors) > 0 {
		s.Neighbors = fmt.Sprintf("[%s] same-ssid: %s", s.Desc, strings.Join(neighbors, "; "))
	}
	return s
}

func bssExtra(b *wlan.BSS) string {
	ie := wlan.ParseIEs(b.IEs)
	util, sta := "n/a", "n/a"
	if ie.Util >= 0 {
		util = fmt.Sprintf("%d%%", ie.Util)
	}
	if ie.Stations >= 0 {
		sta = fmt.Sprint(ie.Stations)
	}
	return fmt.Sprintf("util=%s sta=%s caps=%s", util, sta, ie.Caps())
}

// hasChanged reports whether this poll differs enough from the last logged one.
func hasChanged(samples []ifaceSample, last map[string]ifaceSample, cfg Config) bool {
	if len(samples) != len(last) {
		return true // an interface arrived or went away
	}
	for _, s := range samples {
		prev, found := last[s.Key]
		switch {
		case !found, prev.Sig != s.Sig:
			return true
		case s.HasRSSI && abs(s.RSSI-prev.RSSI) >= cfg.RSSIThreshold:
			return true
		case rateChanged(prev.Rx, s.Rx, cfg.RateChangePercent), rateChanged(prev.Tx, s.Tx, cfg.RateChangePercent):
			return true
		case s.HandshakeFails > 0:
			return true
		case pctChanged(s.RetryValid, s.RetryPct, prev.HasRefRetry, prev.RefRetryPct, cfg.ErrorPctThreshold),
			pctChanged(s.FCSValid, s.FCSPct, prev.HasRefFCS, prev.RefFCSPct, cfg.ErrorPctThreshold):
			return true
		}
	}
	return false
}

// pctChanged reports whether a valid retry/FCS percentage moved at least threshold
// points from the reference. The first valid value (no reference yet) counts as a
// change, so it gets logged and becomes the reference. threshold 0 disables the check.
func pctChanged(valid bool, cur float64, hasRef bool, ref float64, threshold int) bool {
	if threshold <= 0 || !valid {
		return false
	}
	if !hasRef {
		return true
	}
	return absF(cur-ref) >= float64(threshold)
}

// withReference returns s with its reference percentages set: its own values if
// they are valid, otherwise the ones carried over from the previous logged sample.
func withReference(s, prev ifaceSample) ifaceSample {
	s.RefRetryPct, s.HasRefRetry = prev.RefRetryPct, prev.HasRefRetry
	s.RefFCSPct, s.HasRefFCS = prev.RefFCSPct, prev.HasRefFCS
	if s.RetryValid {
		s.RefRetryPct, s.HasRefRetry = s.RetryPct, true
	}
	if s.FCSValid {
		s.RefFCSPct, s.HasRefFCS = s.FCSPct, true
	}
	return s
}

// rateChanged reports whether cur differs from prev by at least percent of prev.
func rateChanged(prev, cur, percent int) bool {
	if percent <= 0 || prev == cur {
		return false
	}
	if prev == 0 {
		return true
	}
	return int64(abs(cur-prev))*100/int64(prev) >= int64(percent)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
