package wlan

import (
	"testing"
	"unsafe"
)

// These numbers are the byte offsets from the Windows SDK headers (also written in
// the C# reference). If a struct mirror is wrong, these fail.
func TestStructLayout(t *testing.T) {
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"sizeof WLAN_INTERFACE_INFO", unsafe.Sizeof(interfaceInfo{}), 532},
		{"WLAN_INTERFACE_INFO_LIST.InterfaceInfo", unsafe.Offsetof(interfaceInfoList{}.InterfaceInfo), 8},
		{"WLAN_CONNECTION_ATTRIBUTES assoc SSID", unsafe.Offsetof(connectionAttributes{}.Association), 520},
		{"WLAN_CONNECTION_ATTRIBUTES BSSID", unsafe.Offsetof(connectionAttributes{}.Association) + unsafe.Offsetof(associationAttributes{}.BSSID), 560},
		{"WLAN_CONNECTION_ATTRIBUTES PhyType", unsafe.Offsetof(connectionAttributes{}.Association) + unsafe.Offsetof(associationAttributes{}.PhyType), 568},
		{"WLAN_CONNECTION_ATTRIBUTES RxRate", unsafe.Offsetof(connectionAttributes{}.Association) + unsafe.Offsetof(associationAttributes{}.RxRate), 580},
		{"WLAN_CONNECTION_ATTRIBUTES TxRate", unsafe.Offsetof(connectionAttributes{}.Association) + unsafe.Offsetof(associationAttributes{}.TxRate), 584},
		{"sizeof WLAN_BSS_ENTRY", unsafe.Sizeof(bssEntry{}), 360},
		{"WLAN_BSS_ENTRY.BSSID", unsafe.Offsetof(bssEntry{}.BSSID), 40},
		{"WLAN_BSS_ENTRY.lRssi", unsafe.Offsetof(bssEntry{}.RSSI), 56},
		{"WLAN_BSS_ENTRY.ulChCenterFrequency", unsafe.Offsetof(bssEntry{}.ChCenterFrequency), 92},
		{"WLAN_BSS_ENTRY.ulIeOffset", unsafe.Offsetof(bssEntry{}.IEOffset), 352},
		{"WLAN_BSS_ENTRY.ulIeSize", unsafe.Offsetof(bssEntry{}.IESize), 356},
		{"WLAN_BSS_LIST.Entries", unsafe.Offsetof(bssList{}.Entries), 8},
		{"WLAN_STATISTICS.MacUcastCounters", unsafe.Offsetof(statistics{}.MacUcastCounters), 24},
		{"WLAN_STATISTICS.dwNumberOfPhys", unsafe.Offsetof(statistics{}.NumberOfPhys), 216},
		{"WLAN_STATISTICS.PhyCounters", unsafe.Offsetof(statistics{}.PhyCounters), 224},
		{"sizeof WLAN_PHY_FRAME_STATISTICS", unsafe.Sizeof(phyFrameStatistics{}), 144},
		{"WLAN_PHY_FRAME_STATISTICS.FCSErrorCount", unsafe.Offsetof(phyFrameStatistics{}.FCSErrorCount), 136},
		{"WLAN_NOTIFICATION_DATA.pData", unsafe.Offsetof(notificationData{}.Data), 32},
		{"WLAN_CONNECTION_NOTIFICATION_DATA.SSID", unsafe.Offsetof(connectionNotificationData{}.SSID), 516},
		{"WLAN_CONNECTION_NOTIFICATION_DATA.ReasonCode", unsafe.Offsetof(connectionNotificationData{}.ReasonCode), 560},
		{"WLAN_MSM_NOTIFICATION_DATA.MacAddr", unsafe.Offsetof(msmNotificationData{}.MacAddr), 556},
		{"WLAN_MSM_NOTIFICATION_DATA.ReasonCode", unsafe.Offsetof(msmNotificationData{}.ReasonCode), 576},
		{"sizeof WLAN_PHY_RADIO_STATE", unsafe.Sizeof(phyRadioState{}), 12},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestFreqToChannel(t *testing.T) {
	tests := []struct {
		khz     uint32
		channel int
		band    string
	}{
		{2412000, 1, "2.4GHz"},
		{2437000, 6, "2.4GHz"},
		{2472000, 13, "2.4GHz"},
		{2484000, 14, "2.4GHz"},
		{5180000, 36, "5GHz"},
		{5745000, 149, "5GHz"},
		{5955000, 1, "6GHz"},
		{6115000, 33, "6GHz"},
		{900000, 0, "900MHz"},
	}
	for _, tt := range tests {
		ch, band := FreqToChannel(tt.khz)
		if ch != tt.channel || band != tt.band {
			t.Errorf("FreqToChannel(%d) = %d, %q; want %d, %q", tt.khz, ch, band, tt.channel, tt.band)
		}
	}
}

func TestParseIEs(t *testing.T) {
	tests := []struct {
		name string
		ies  []byte
		want IEInfo
		caps string
	}{
		{"empty", nil, IEInfo{Util: -1, Stations: -1}, "-"},
		{"bss load", []byte{11, 5, 0x0C, 0x01, 255, 0, 0}, IEInfo{Util: 100, Stations: 268}, "-"},
		{"11r 11k 11v", []byte{54, 3, 1, 2, 3, 70, 5, 0, 0, 0, 0, 0, 127, 3, 0, 0, 0x08},
			IEInfo{Util: -1, Stations: -1, FT11r: true, RRM11k: true, BTM11v: true}, "11r/11k/11v"},
		{"ext caps without BTM", []byte{127, 3, 0, 0, 0x04}, IEInfo{Util: -1, Stations: -1}, "-"},
		{"truncated element stops parsing", []byte{70, 9, 1, 2}, IEInfo{Util: -1, Stations: -1}, "-"},
	}
	for _, tt := range tests {
		got := ParseIEs(tt.ies)
		if got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
		if got.Caps() != tt.caps {
			t.Errorf("%s: caps %q, want %q", tt.name, got.Caps(), tt.caps)
		}
	}
}
