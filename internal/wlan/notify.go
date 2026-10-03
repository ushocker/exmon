package wlan

import (
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Notification is a copy of one WLAN_NOTIFICATION_DATA. Data holds the raw payload,
// copied out because Windows frees it as soon as the callback returns.
type Notification struct {
	Source    uint32 // SourceACM or SourceMSM
	Code      uint32
	Interface windows.GUID
	Data      []byte
}

// ConnectionInfo is the payload of the ACM connection_* / disconnect* notifications.
type ConnectionInfo struct {
	Profile    string
	SSID       string
	ReasonCode uint32
}

// MSMInfo is the payload carried by most MSM notifications.
type MSMInfo struct {
	Profile    string
	SSID       string
	BSSID      string
	ReasonCode uint32
}

// RadioState is the payload of MSM radio_state_change.
type RadioState struct {
	PhyIndex uint32
	Software uint32 // 1 on, 2 off; see RadioStateName
	Hardware uint32
}

// ConnectionInfo decodes the payload as WLAN_CONNECTION_NOTIFICATION_DATA.
func (n Notification) ConnectionInfo() (ConnectionInfo, bool) {
	d, ok := payloadAs[connectionNotificationData](n.Data)
	if !ok {
		return ConnectionInfo{}, false
	}
	return ConnectionInfo{
		Profile:    windows.UTF16ToString(d.ProfileName[:]),
		SSID:       ssidString(d.SSID),
		ReasonCode: d.ReasonCode,
	}, true
}

// MSMInfo decodes the payload as WLAN_MSM_NOTIFICATION_DATA.
func (n Notification) MSMInfo() (MSMInfo, bool) {
	d, ok := payloadAs[msmNotificationData](n.Data)
	if !ok {
		return MSMInfo{}, false
	}
	return MSMInfo{
		Profile:    windows.UTF16ToString(d.ProfileName[:]),
		SSID:       ssidString(d.SSID),
		BSSID:      FormatMAC(d.MacAddr),
		ReasonCode: d.ReasonCode,
	}, true
}

// RadioState decodes the payload as WLAN_PHY_RADIO_STATE.
func (n Notification) RadioState() (RadioState, bool) {
	d, ok := payloadAs[phyRadioState](n.Data)
	if !ok {
		return RadioState{}, false
	}
	return RadioState{PhyIndex: d.PhyIndex, Software: d.SoftwareState, Hardware: d.HardwareState}, true
}

// payloadAs copies the start of b into a T, if b is big enough.
// [T any] makes this a generic function: it works for any struct type T.
func payloadAs[T any](b []byte) (T, bool) {
	var v T
	if uintptr(len(b)) < unsafe.Sizeof(v) {
		return v, false
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&v)), unsafe.Sizeof(v)), b)
	return v, true
}

// subscriber is where the callback sends notifications. atomic.Pointer lets the
// callback (on a Windows thread) and Subscribe/Unsubscribe (on ours) share it safely.
var (
	subscriber atomic.Pointer[chan<- Notification]
	dropped    atomic.Uint64
)

// Dropped returns how many notifications were thrown away because the channel was full.
func Dropped() uint64 { return dropped.Load() }

// notifyCallback runs on a thread owned by the WLAN service, so it must never block.
// It is created once: Go cannot free callbacks made with windows.NewCallback.
var notifyCallback = windows.NewCallback(func(d *notificationData, _ uintptr) uintptr {
	ch := subscriber.Load()
	if d == nil || ch == nil {
		return 0
	}
	n := Notification{Source: d.Source, Code: d.Code, Interface: d.InterfaceGUID}
	if d.Data != nil && d.DataSize > 0 {
		n.Data = append([]byte(nil), unsafe.Slice((*byte)(d.Data), d.DataSize)...)
	}
	// A select with a default case never waits: if the channel is full, count a drop.
	select {
	case *ch <- n:
	default:
		dropped.Add(1)
	}
	return 0
})

// Subscribe registers for ACM and MSM notifications and delivers them to ch.
// Only one subscriber is supported at a time. ch should be buffered.
func (c *Client) Subscribe(ch chan<- Notification) error {
	subscriber.Store(&ch)
	r, _, _ := procWlanRegisterNotification.Call(
		uintptr(c.handle),
		SourceACM|SourceMSM,
		1, // bIgnoreDuplicate
		notifyCallback,
		0, 0, 0,
	)
	if err := check("WlanRegisterNotification", r); err != nil {
		subscriber.Store(nil)
		return err
	}
	return nil
}

// Unsubscribe stops notifications (by registering for no sources).
func (c *Client) Unsubscribe() {
	procWlanRegisterNotification.Call(uintptr(c.handle), SourceNone, 0, 0, 0, 0, 0)
	subscriber.Store(nil)
}
