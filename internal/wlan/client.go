package wlan

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Interface is one wireless adapter.
type Interface struct {
	GUID        windows.GUID
	Description string
	State       uint32 // WLAN_INTERFACE_STATE; see IfStateName
}

// Connection is the current association of a connected interface.
type Connection struct {
	Profile       string
	SSID          string
	BSSID         string // "AA:BB:CC:DD:EE:FF"
	PhyType       uint32 // DOT11_PHY_TYPE; see PhyTypeName
	SignalQuality uint32 // 0-100
	RxRate        uint32 // kbps
	TxRate        uint32 // kbps
	AuthAlgorithm uint32 // DOT11_AUTH_ALGORITHM; see AuthName
	Cipher        uint32 // DOT11_CIPHER_ALGORITHM; see CipherName
}

// BSS is one access point (radio) from the scan cache.
type BSS struct {
	SSID    string
	BSSID   string
	RSSI    int    // dBm
	FreqKHz uint32 // channel centre frequency
	IEs     []byte // raw beacon/probe information elements; see ParseIEs
}

// Counters are the cumulative 802.11 frame counters from WLAN_STATISTICS,
// summed across all PHYs. They only ever grow, until the adapter resets.
type Counters struct {
	HandshakeFail int64
	DecryptFail   int64
	TxFrames      int64
	RxFrames      int64
	TxFailed      int64
	Retry         int64
	MultiRetry    int64
	RTSFail       int64
	ACKFail       int64
	FCSErr        int64
}

// Client is an open session with the WLAN service.
type Client struct {
	handle windows.Handle
}

// Open starts a session with the WLAN service (WlanOpenHandle).
func Open() (*Client, error) {
	var negotiated uint32
	var h windows.Handle
	r, _, _ := procWlanOpenHandle.Call(
		clientVersion2,
		0,
		uintptr(unsafe.Pointer(&negotiated)),
		uintptr(unsafe.Pointer(&h)),
	)
	if err := check("WlanOpenHandle", r); err != nil {
		return nil, err
	}
	return &Client{handle: h}, nil
}

// Close unsubscribes from notifications and closes the session. Safe to call twice.
func (c *Client) Close() error {
	if c.handle == 0 {
		return nil
	}
	c.Unsubscribe()
	r, _, _ := procWlanCloseHandle.Call(uintptr(c.handle), 0)
	c.handle = 0
	return check("WlanCloseHandle", r)
}

// Interfaces lists the wireless adapters (WlanEnumInterfaces).
func (c *Client) Interfaces() ([]Interface, error) {
	var list *interfaceInfoList
	r, _, _ := procWlanEnumInterfaces.Call(uintptr(c.handle), 0, uintptr(unsafe.Pointer(&list)))
	if err := check("WlanEnumInterfaces", r); err != nil {
		return nil, err
	}
	defer freeMemory(unsafe.Pointer(list))

	// unsafe.Slice views the C array that follows the header as a Go slice.
	items := unsafe.Slice(&list.InterfaceInfo[0], list.NumberOfItems)
	out := make([]Interface, 0, len(items))
	for i := range items {
		out = append(out, Interface{
			GUID:        items[i].InterfaceGUID,
			Description: windows.UTF16ToString(items[i].Description[:]),
			State:       items[i].State,
		})
	}
	return out, nil
}

// CurrentConnection reads the association of a connected interface.
func (c *Client) CurrentConnection(guid windows.GUID) (Connection, error) {
	p, _, err := c.queryInterface(guid, opcodeCurrentConnection)
	if err != nil {
		return Connection{}, err
	}
	defer freeMemory(p)

	attrs := (*connectionAttributes)(p)
	a := &attrs.Association
	return Connection{
		Profile:       windows.UTF16ToString(attrs.ProfileName[:]),
		SSID:          ssidString(a.SSID),
		BSSID:         FormatMAC(a.BSSID),
		PhyType:       a.PhyType,
		SignalQuality: a.SignalQuality,
		RxRate:        a.RxRate,
		TxRate:        a.TxRate,
		AuthAlgorithm: attrs.Security.AuthAlgorithm,
		Cipher:        attrs.Security.CipherAlgorithm,
	}, nil
}

// Statistics reads the frame counters. Many drivers do not support this and return an error.
func (c *Client) Statistics(guid windows.GUID) (Counters, error) {
	p, size, err := c.queryInterface(guid, opcodeStatistics)
	if err != nil {
		return Counters{}, err
	}
	defer freeMemory(p)

	header := unsafe.Offsetof(statistics{}.PhyCounters)
	if uintptr(size) < header {
		return Counters{}, fmt.Errorf("WLAN_STATISTICS too small: %d bytes", size)
	}
	s := (*statistics)(p)
	out := Counters{
		HandshakeFail: int64(s.FourWayHandshakeFailures),
		DecryptFail:   int64(s.MacUcastCounters.CCMPDecryptErrors + s.MacUcastCounters.DecryptFailureCount),
	}
	// Only read as many PHY entries as actually fit in the returned buffer.
	fit := (uintptr(size) - header) / unsafe.Sizeof(phyFrameStatistics{})
	n := min(uintptr(s.NumberOfPhys), fit)
	if n == 0 {
		return out, nil
	}
	for _, ph := range unsafe.Slice(&s.PhyCounters[0], n) {
		out.TxFrames += int64(ph.TransmittedFrameCount)
		out.TxFailed += int64(ph.FailedCount)
		out.Retry += int64(ph.RetryCount)
		out.MultiRetry += int64(ph.MultipleRetryCount)
		out.RTSFail += int64(ph.RTSFailureCount)
		out.ACKFail += int64(ph.ACKFailureCount)
		out.RxFrames += int64(ph.ReceivedFrameCount)
		out.FCSErr += int64(ph.FCSErrorCount)
	}
	return out, nil
}

// BssList returns every BSS in the interface's scan cache (WlanGetNetworkBssList).
func (c *Client) BssList(guid windows.GUID) ([]BSS, error) {
	var list *bssList
	r, _, _ := procWlanGetNetworkBssList.Call(
		uintptr(c.handle),
		uintptr(unsafe.Pointer(&guid)),
		0, // any SSID
		dot11BssTypeAny,
		0, // bSecurityEnabled: ignored for "any"
		0,
		uintptr(unsafe.Pointer(&list)),
	)
	if err := check("WlanGetNetworkBssList", r); err != nil {
		return nil, err
	}
	defer freeMemory(unsafe.Pointer(list))

	if list.NumberOfItems == 0 {
		return nil, nil
	}
	entries := unsafe.Slice(&list.Entries[0], list.NumberOfItems)
	out := make([]BSS, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		b := BSS{
			SSID:    ssidString(e.SSID),
			BSSID:   FormatMAC(e.BSSID),
			RSSI:    int(e.RSSI),
			FreqKHz: e.ChCenterFrequency,
		}
		if e.IEOffset > 0 && e.IESize > 0 {
			// The IEs sit IEOffset bytes after the start of this entry. Copy them out
			// because the list is freed when we return.
			start := unsafe.Add(unsafe.Pointer(e), e.IEOffset)
			b.IEs = append([]byte(nil), unsafe.Slice((*byte)(start), e.IESize)...)
		}
		out = append(out, b)
	}
	return out, nil
}

// ReasonText turns a WLAN_REASON_CODE into readable text, or "" if Windows doesn't know it.
func ReasonText(code uint32) string {
	var buf [512]uint16
	r, _, _ := procWlanReasonCodeToString.Call(
		uintptr(code),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&buf[0])),
		0,
	)
	if r != 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}

// queryInterface wraps WlanQueryInterface. The caller must freeMemory the result.
func (c *Client) queryInterface(guid windows.GUID, opcode uint32) (unsafe.Pointer, uint32, error) {
	var size uint32
	var data unsafe.Pointer
	r, _, _ := procWlanQueryInterface.Call(
		uintptr(c.handle),
		uintptr(unsafe.Pointer(&guid)),
		uintptr(opcode),
		0,
		uintptr(unsafe.Pointer(&size)),
		uintptr(unsafe.Pointer(&data)),
		0, // value type: not needed
	)
	if err := check("WlanQueryInterface", r); err != nil {
		return nil, 0, err
	}
	return data, size, nil
}

func freeMemory(p unsafe.Pointer) {
	procWlanFreeMemory.Call(uintptr(p))
}

func ssidString(s dot11SSID) string {
	n := min(s.Length, uint32(len(s.SSID)))
	return string(s.SSID[:n])
}

// FormatMAC formats a MAC address as upper-case "AA:BB:CC:DD:EE:FF".
func FormatMAC(m [6]byte) string {
	return fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", m[0], m[1], m[2], m[3], m[4], m[5])
}
