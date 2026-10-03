package wifi

import (
	"fmt"

	"exmon/internal/wlan"
)

// delta is the change in a cumulative counter. Counters restart when the adapter
// resets, so a negative delta means "everything counted since the reset".
func delta(cur, prev int64) int64 {
	if d := cur - prev; d >= 0 {
		return d
	}
	return cur
}

// statDelta is how much each frame counter grew between two samples.
type statDelta struct {
	tx, rx, retry, multi, failed, ack, rts, fcs, hs, dec int64
}

func diffStats(c, p wlan.Counters) statDelta {
	return statDelta{
		tx: delta(c.TxFrames, p.TxFrames), rx: delta(c.RxFrames, p.RxFrames),
		retry: delta(c.Retry, p.Retry), multi: delta(c.MultiRetry, p.MultiRetry),
		failed: delta(c.TxFailed, p.TxFailed), ack: delta(c.ACKFail, p.ACKFail),
		rts: delta(c.RTSFail, p.RTSFail), fcs: delta(c.FCSErr, p.FCSErr),
		hs: delta(c.HandshakeFail, p.HandshakeFail), dec: delta(c.DecryptFail, p.DecryptFail),
	}
}

// retryPct is retries as a percentage of transmitted frames.
func (d statDelta) retryPct() float64 { return pct(d.retry, d.tx) }

// fcsPct is frames with a bad checksum as a percentage of all received frames.
func (d statDelta) fcsPct() float64 { return pct(d.fcs, d.rx+d.fcs) }

// retryValid reports whether retryPct is worth acting on: enough frames were sent
// for a percentage to mean something, and the driver's counters are believable.
func (d statDelta) retryValid(minFrames int, inconsistent bool) bool {
	return !inconsistent && d.tx >= int64(minFrames) && d.tx > 0
}

// fcsValid is the same check for fcsPct, which is measured over received frames.
func (d statDelta) fcsValid(minFrames int, inconsistent bool) bool {
	return !inconsistent && d.rx+d.fcs >= int64(minFrames) && d.rx+d.fcs > 0
}

// computeStatDeltas fills the retry/FCS fields and StatsText for each sample,
// measured against the last *logged* sample. That way a STATS line always covers
// exactly the time since the previous STATS line.
func computeStatDeltas(samples []ifaceSample, last map[string]ifaceSample, minFrames int, inconsistent map[string]bool) {
	for i := range samples {
		s := &samples[i]
		prev, found := last[s.Key]
		if s.Stats == nil || !found || prev.Stats == nil {
			continue
		}
		d := diffStats(*s.Stats, *prev.Stats)
		s.RetryPct = d.retryPct()
		s.FCSPct = d.fcsPct()
		s.RetryValid = d.retryValid(minFrames, inconsistent[s.Key])
		s.FCSValid = d.fcsValid(minFrames, inconsistent[s.Key])
		s.HandshakeFails = d.hs

		// Raw numbers are always printed; a note says when the percentages aren't used.
		note := ""
		switch {
		case inconsistent[s.Key]:
			note = " [counters inconsistent: % ignored]"
		case !s.RetryValid:
			note = fmt.Sprintf(" [retry%% ignored: tx < %d frames]", minFrames)
		}
		s.StatsText = fmt.Sprintf(
			"[%s] tx=%d rx=%d retry=%d (%.1f%%) multi_retry=%d tx_fail=%d ack_fail=%d rts_fail=%d fcs_err=%d (%.1f%%) handshake_fail=%d decrypt_fail=%d%s",
			s.Desc, d.tx, d.rx, d.retry, s.RetryPct, d.multi, d.failed, d.ack, d.rts, d.fcs, s.FCSPct, d.hs, d.dec, note)
	}
}

func pct(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}

// statsPtr returns a pointer to a copy of c, so a sample can say "no stats" with nil.
func statsPtr(c wlan.Counters) *wlan.Counters { return &c }
