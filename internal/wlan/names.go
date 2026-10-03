package wlan

import "strconv"

// ACM notification codes (WLAN_NOTIFICATION_ACM), indexed by code.
var acmNames = []string{"", "autoconf_enabled", "autoconf_disabled", "background_scan_enabled",
	"background_scan_disabled", "bss_type_change", "power_setting_change", "scan_complete", "scan_fail",
	"connection_start", "connection_complete", "connection_attempt_fail", "filter_list_change",
	"interface_arrival", "interface_removal", "profile_change", "profile_name_change", "profiles_exhausted",
	"network_not_available", "network_available", "disconnecting", "disconnected", "adhoc_network_state_change",
	"profile_unblocked", "screen_power_change", "profile_blocked", "scan_list_refresh", "operational_state_change"}

// MSM notification codes (WLAN_NOTIFICATION_MSM), indexed by code.
var msmNames = []string{"", "associating", "associated", "authenticating", "connected",
	"roaming_start", "roaming_end", "radio_state_change", "signal_quality_change", "disassociating",
	"disconnected", "peer_join", "peer_leave", "adapter_removal", "adapter_operation_mode_change",
	"link_degraded", "link_improved"}

// ACM codes whose payload is WLAN_CONNECTION_NOTIFICATION_DATA.
const (
	ACMConnectionStart       = 9
	ACMConnectionComplete    = 10
	ACMConnectionAttemptFail = 11
	ACMDisconnecting         = 20
	ACMDisconnected          = 21
)

// MSM codes that need special handling.
const (
	MSMRadioStateChange    = 7
	MSMSignalQualityChange = 8
)

// ACMName returns the name of an ACM code, or the number if unknown.
func ACMName(code uint32) string { return lookup(acmNames, code) }

// MSMName returns the name of an MSM code, or the number if unknown.
func MSMName(code uint32) string { return lookup(msmNames, code) }

func lookup(names []string, code uint32) string {
	if int(code) < len(names) && names[code] != "" {
		return names[code]
	}
	return strconv.FormatUint(uint64(code), 10)
}

// IfStateName names a WLAN_INTERFACE_STATE.
func IfStateName(s uint32) string {
	switch s {
	case 0:
		return "not_ready"
	case 1:
		return "connected"
	case 2:
		return "ad_hoc_formed"
	case 3:
		return "disconnecting"
	case 4:
		return "disconnected"
	case 5:
		return "associating"
	case 6:
		return "discovering"
	case 7:
		return "authenticating"
	}
	return strconv.FormatUint(uint64(s), 10)
}

// PhyTypeName names a DOT11_PHY_TYPE.
func PhyTypeName(t uint32) string {
	switch t {
	case 4:
		return "802.11b"
	case 5:
		return "802.11a"
	case 6:
		return "802.11g"
	case 7:
		return "802.11n"
	case 8:
		return "802.11ac"
	case 9:
		return "802.11ad"
	case 10:
		return "802.11ax"
	case 11:
		return "802.11be"
	}
	return "phy" + strconv.FormatUint(uint64(t), 10)
}

// RadioStateName names a DOT11_RADIO_STATE.
func RadioStateName(s uint32) string {
	switch s {
	case 1:
		return "on"
	case 2:
		return "off"
	}
	return "unknown"
}
