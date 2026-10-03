// Package wlan is a thin, typed wrapper over the Windows Native Wifi API (wlanapi.dll).
//
// This is the only package that uses unsafe/uintptr. Everything it returns is a plain
// Go value, and any memory Windows allocates is freed before a function returns.
package wlan

import "golang.org/x/sys/windows"

// The DLL and each function are looked up lazily, on first use.
var (
	wlanapi = windows.NewLazySystemDLL("wlanapi.dll")

	procWlanOpenHandle           = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle          = wlanapi.NewProc("WlanCloseHandle")
	procWlanRegisterNotification = wlanapi.NewProc("WlanRegisterNotification")
	procWlanReasonCodeToString   = wlanapi.NewProc("WlanReasonCodeToString")
	procWlanEnumInterfaces       = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface       = wlanapi.NewProc("WlanQueryInterface")
	procWlanGetNetworkBssList    = wlanapi.NewProc("WlanGetNetworkBssList")
	procWlanFreeMemory           = wlanapi.NewProc("WlanFreeMemory")
)

// Constants from wlanapi.h / windot11.h.
const (
	clientVersion2 = 2 // Vista and later

	opcodeCurrentConnection = 7          // wlan_intf_opcode_current_connection
	opcodeStatistics        = 0x10000101 // wlan_intf_opcode_statistics

	dot11BssTypeAny = 3

	// Notification sources (WLAN_NOTIFICATION_SOURCE_*).
	SourceNone = 0x00
	SourceACM  = 0x08 // auto configuration module: connect, disconnect, scan
	SourceMSM  = 0x10 // media specific module: associate, roam, radio, link quality

	// InterfaceStateConnected is wlan_interface_state_connected.
	InterfaceStateConnected = 1
)
