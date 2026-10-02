package wlanapi


import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"time"
	"unsafe"
 
	"golang.org/x/sys/windows"
)
 
// Load the DLL and look up the functions we need (lazily, on first call).
var (
	wlanapi                			= windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle     			= wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle    			= wlanapi.NewProc("WlanCloseHandle")
	procWlanReasonCodeToString		= wlanapi.NewProc("WlanReasonCodeToString")
	procWlanEnumInterfaces			= wlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface 			= wlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMemory     			= wlanapi.NewProc("WlanFreeMemory")
	procWlanGetNetworkBssList    	= wlanapi.NewProc("WlanGetNetworkBssList")
	procWlanRegisterNotification 	= wlanapi.NewProc("WlanRegisterNotification")
)
 
const (
	wlanClientVersion2              = 2 // Vista and later
	wlanIntfOpcodeCurrentConnection = 7 // WLAN_INTF_OPCODE value
	wlanInterfaceStateConnected     = 1
)
 
const (
	wlanNotificationSourceNone = 0x00 // used to unregister
	wlanNotificationSourceACM  = 0x08 // auto config module: connect/disconnect/scan
	wlanNotificationSourceMSM  = 0x10 // media specific module: roaming/signal/link
)
 
// WLAN_NOTIFICATION_ACM codes we care about.
const (
	acmScanFail              = 8
	acmConnectionStart       = 9
	acmConnectionComplete    = 10
	acmConnectionAttemptFail = 11
	acmDisconnecting         = 20
	acmDisconnected          = 21
)
 
// WLAN_NOTIFICATION_MSM codes we care about.
const (
	msmRoamingStart        = 5
	msmRoamingEnd          = 6
	msmSignalQualityChange = 8
	msmLinkDegraded        = 15
	msmLinkImproved        = 16
)

type wlanInterfaceInfo struct { // WLAN_INTERFACE_INFO
	InterfaceGUID windows.GUID
	Description   [256]uint16 // WCHAR[256]
	State         uint32
}
 
type wlanInterfaceInfoList struct { // WLAN_INTERFACE_INFO_LIST
	NumberOfItems uint32
	Index         uint32
	InterfaceInfo [1]wlanInterfaceInfo // really NumberOfItems long
}
 
type dot11SSID struct { // DOT11_SSID
	Length uint32
	SSID   [32]byte
}
 
type wlanAssociationAttributes struct { // WLAN_ASSOCIATION_ATTRIBUTES
	SSID          dot11SSID
	BssType       uint32
	BSSID         [6]byte // access point MAC
	PhyType       uint32  // 802.11a/b/g/n/ac/ax
	PhyIndex      uint32
	SignalQuality uint32 // 0-100
	RxRate        uint32 // kbps
	TxRate        uint32 // kbps
}
 
type wlanSecurityAttributes struct { // WLAN_SECURITY_ATTRIBUTES
	SecurityEnabled int32
	OneXEnabled     int32
	AuthAlgorithm   uint32
	CipherAlgorithm uint32
}
 
type wlanConnectionAttributes struct { // WLAN_CONNECTION_ATTRIBUTES
	State          uint32
	ConnectionMode uint32
	ProfileName    [256]uint16
	Association    wlanAssociationAttributes
	Security       wlanSecurityAttributes
}

type wlanNotificationData struct {
	Source        uint32
	Code          uint32
	InterfaceGUID windows.GUID
	DataSize      uint32
	Data          unsafe.Pointer
}

type WifiEvent struct {
	Time          time.Time
	Source        uint32
	Code          uint32
	Interface     windows.GUID
	SignalQuality uint32 // set only for signal quality changes
	HasSignal     bool
}

var (
	events  = make(chan WifiEvent, 256) // buffered so the callback never waits
	dropped atomic.Uint64               // count events lost if the buffer fills
)

var notificationCallback = windows.NewCallback(
	func(data *wlanNotificationData, context uintptr) uintptr {
		if data == nil {
			return 0
		}
		ev := WifiEvent{
			Time:      time.Now(),
			Source:    data.Source,
			Code:      data.Code,
			Interface: data.InterfaceGUID,
		}
		if data.Source == wlanNotificationSourceMSM &&
			data.Code == msmSignalQualityChange &&
			data.DataSize >= 4 && data.Data != nil {
			ev.SignalQuality = *(*uint32)(data.Data)
			ev.HasSignal = true
		}
		select {
		case events <- ev:
		default:
			dropped.Add(1)
		}
		return 0
	})


func describe(ev WifiEvent) string {
	switch ev.Source {
	case wlanNotificationSourceACM:
		switch ev.Code {
		case acmScanFail:
			return "scan failed"
		case acmConnectionStart:
			return "connecting"
		case acmConnectionComplete:
			return "connected"
		case acmConnectionAttemptFail:
			return "connection attempt FAILED"
		case acmDisconnecting:
			return "disconnecting"
		case acmDisconnected:
			return "DISCONNECTED"
		}
	case wlanNotificationSourceMSM:
		switch ev.Code {
		case msmRoamingStart:
			return "roaming started"
		case msmRoamingEnd:
			return "roamed to new access point"
		case msmSignalQualityChange:
			return fmt.Sprintf("signal quality now %d%%", ev.SignalQuality)
		case msmLinkDegraded:
			return "link DEGRADED"
		case msmLinkImproved:
			return "link improved"
		}
	}
	return "" // an event we don't track
}

func eventsListener (handle windows.Handle){

	r, _, _ := procWlanRegisterNotification.Call(
		uintptr(handle),
		wlanNotificationSourceACM|wlanNotificationSourceMSM,
		1, // bIgnoreDuplicate: TRUE
		notificationCallback,
		0, // context pointer (unused; see notes)
		0, // reserved
		0, // previous sources (optional)
	)
	if r != 0 {
		fmt.Println("WlanRegisterNotification failed:", windows.Errno(r))
		return
	}
	// Unregister by registering for "no sources".
	defer procWlanRegisterNotification.Call(uintptr(handle),
		wlanNotificationSourceNone, 0, 0, 0, 0, 0)

	fmt.Println("Listening for Wi-Fi events. Ctrl+C to stop.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
 
	for {
		select {
		case ev := <-events:
			if msg := describe(ev); msg != "" {
				fmt.Printf("%s  %s\n", ev.Time.Format("15:04:05.000"), msg)
			}
		case <-stop:
			fmt.Printf("Stopping. Events dropped: %d\n", dropped.Load())
			return
		}
	}
}

func Query() {
	// 1. Open a session with the WLAN service.
	var negotiated uint32
	var handle windows.Handle
	r, _, _ := procWlanOpenHandle.Call(
		wlanClientVersion2,
		0,
		uintptr(unsafe.Pointer(&negotiated)),
		uintptr(unsafe.Pointer(&handle)),
	)
	if r != 0 { // return value is a Win32 error code; 0 = success
		fmt.Println("WlanOpenHandle failed:", windows.Errno(r))
		return
	}
	defer procWlanCloseHandle.Call(uintptr(handle), 0)
	
	eventsListener(handle)

	// 2. List wireless adapters. Windows allocates the list; we must free it.
	var list *wlanInterfaceInfoList
	r, _, _ = procWlanEnumInterfaces.Call(uintptr(handle), 0, uintptr(unsafe.Pointer(&list)))
	if r != 0 {
		fmt.Println("WlanEnumInterfaces failed:", windows.Errno(r))
		return
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(list)))
 
	// View the variable-length C array as a Go slice.
	ifaces := unsafe.Slice(&list.InterfaceInfo[0], list.NumberOfItems)
 
	for i := range ifaces {
		iface := &ifaces[i]
		name := windows.UTF16ToString(iface.Description[:])
 
		if iface.State != wlanInterfaceStateConnected {
			fmt.Printf("%s: not connected\n", name)
			continue
		}
 
		// 3. Ask for the current connection's attributes.
		var size, valueType uint32
		var attrs *wlanConnectionAttributes
		r, _, _ := procWlanQueryInterface.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(&iface.InterfaceGUID)),
			wlanIntfOpcodeCurrentConnection,
			0,
			uintptr(unsafe.Pointer(&size)),
			uintptr(unsafe.Pointer(&attrs)),
			uintptr(unsafe.Pointer(&valueType)),
		)
		if r != 0 {
			fmt.Printf("%s: query failed: %v\n", name, windows.Errno(r))
			continue
		}
 
		a := attrs.Association
		ssidLen := min(a.SSID.Length, 32)
		quality := a.SignalQuality
 
		fmt.Printf("Adapter:  %s\n", name)
		fmt.Printf("SSID:     %s\n", string(a.SSID.SSID[:ssidLen]))
		fmt.Printf("BSSID:    %s\n", net.HardwareAddr(a.BSSID[:]))
		fmt.Printf("Signal:   %d%% (~%d dBm)\n", quality, int(quality)/2-100)
		fmt.Printf("Rx / Tx:  %.1f / %.1f Mbps\n", float64(a.RxRate)/1000, float64(a.TxRate)/1000)
		fmt.Println()
 
		procWlanFreeMemory.Call(uintptr(unsafe.Pointer(attrs)))
	}
}