package wifi

import (
	"fmt"

	"exmon/internal/wlan"
)

// describe turns a notification into a log kind and detail.
// ok is false for notifications we don't log.
func describe(n wlan.Notification) (kind, detail string, ok bool) {
	switch n.Source {
	case wlan.SourceACM:
		kind = "ACM/" + wlan.ACMName(n.Code)
		switch n.Code {
		case wlan.ACMConnectionStart, wlan.ACMConnectionComplete, wlan.ACMConnectionAttemptFail,
			wlan.ACMDisconnecting, wlan.ACMDisconnected:
			if c, ok := n.ConnectionInfo(); ok {
				detail = fmt.Sprintf("profile=%q ssid=%q reason=%d %q",
					c.Profile, c.SSID, c.ReasonCode, wlan.ReasonText(c.ReasonCode))
			}
		}
		return kind, detail, true

	case wlan.SourceMSM:
		kind = "MSM/" + wlan.MSMName(n.Code)
		switch n.Code {
		case wlan.MSMSignalQualityChange:
			return "", "", false // the change-driven POLL line already covers this
		case wlan.MSMRadioStateChange:
			if r, ok := n.RadioState(); ok {
				detail = fmt.Sprintf("phy=%d hw=%s sw=%s",
					r.PhyIndex, wlan.RadioStateName(r.Hardware), wlan.RadioStateName(r.Software))
			}
		default: // everything else carries WLAN_MSM_NOTIFICATION_DATA
			if m, ok := n.MSMInfo(); ok {
				detail = fmt.Sprintf("profile=%q ssid=%q bssid=%s reason=%d %q",
					m.Profile, m.SSID, m.BSSID, m.ReasonCode, wlan.ReasonText(m.ReasonCode))
			}
		}
		return kind, detail, true
	}
	return "", "", false
}
