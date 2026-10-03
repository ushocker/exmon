package wlan

import "strings"

// IEInfo is what we extract from a BSS's beacon/probe information elements.
type IEInfo struct {
	Util     int // BSS Load channel utilisation 0-100; -1 if the IE is absent
	Stations int // BSS Load station count; -1 if the IE is absent
	FT11r    bool
	RRM11k   bool
	BTM11v   bool
}

// Caps returns "11r/11k/11v" (only the ones supported), or "-" for none.
func (i IEInfo) Caps() string {
	var c []string
	if i.FT11r {
		c = append(c, "11r")
	}
	if i.RRM11k {
		c = append(c, "11k")
	}
	if i.BTM11v {
		c = append(c, "11v")
	}
	if len(c) == 0 {
		return "-"
	}
	return strings.Join(c, "/")
}

// Information element IDs (IEEE 802.11).
const (
	ieBSSLoad        = 11
	ieMobilityDomain = 54  // 802.11r fast transition
	ieRRMEnabledCaps = 70  // 802.11k
	ieExtendedCaps   = 127 // bit 19 = BSS Transition (802.11v)
)

// ParseIEs walks a list of (id, length, data...) information elements.
func ParseIEs(ies []byte) IEInfo {
	info := IEInfo{Util: -1, Stations: -1}
	for len(ies) >= 2 {
		id, n := ies[0], int(ies[1])
		if 2+n > len(ies) {
			break // truncated element
		}
		data := ies[2 : 2+n]
		switch id {
		case ieBSSLoad: // stationCount(u16 LE) | channelUtilization(u8, out of 255) | admissionCapacity(u16)
			if n >= 3 {
				info.Stations = int(data[0]) | int(data[1])<<8
				info.Util = int(data[2]) * 100 / 255
			}
		case ieMobilityDomain:
			info.FT11r = true
		case ieRRMEnabledCaps:
			info.RRM11k = true
		case ieExtendedCaps:
			if n >= 3 && data[2]&0x08 != 0 {
				info.BTM11v = true
			}
		}
		ies = ies[2+n:]
	}
	return info
}
