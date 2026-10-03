package wlan

import "strconv"

// FreqToChannel converts a centre frequency in kHz to a channel number and band name.
// Unknown frequencies return channel 0 and "<n>MHz".
func FreqToChannel(khz uint32) (channel int, band string) {
	mhz := int(khz / 1000)
	switch {
	case mhz == 2484:
		return 14, "2.4GHz"
	case mhz >= 2412 && mhz < 2484:
		return (mhz - 2407) / 5, "2.4GHz"
	case mhz >= 5150 && mhz <= 5925:
		return (mhz - 5000) / 5, "5GHz"
	case mhz > 5925 && mhz <= 7125:
		return (mhz - 5950) / 5, "6GHz"
	}
	return 0, strconv.Itoa(mhz) + "MHz"
}
