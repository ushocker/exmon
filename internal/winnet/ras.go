package winnet

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const errorBufferTooSmall = 603 // ERROR_BUFFER_TOO_SMALL (RAS error code)

// RASCONNW, Windows 7+ layout. ras.h packs its structs to 4-byte boundaries
// (#include <pshpack4.h>), so the 8-byte handle sits at offset 4 and the struct is
// 1388 bytes on 64-bit. Go would align a uintptr to 8, so the handle is declared as
// [8]byte to get the packed layout. We never use the handle.
type rasConn struct {
	Size       uint32
	Handle     [8]byte
	EntryName  [257]uint16 // RAS_MaxEntryName + 1
	DeviceType [17]uint16  // RAS_MaxDeviceType + 1
	DeviceName [129]uint16 // RAS_MaxDeviceName + 1
	Phonebook  [260]uint16 // MAX_PATH
	SubEntry   uint32
	GUIDEntry  windows.GUID
	Flags      uint32
	LUID       struct {
		Low  uint32
		High int32
	}
	CorrelationID windows.GUID
}

// RasConn is an active built-in Windows VPN (or dial-up) connection.
type RasConn struct {
	EntryName  string // the name shown in Settings > VPN
	DeviceType string // "vpn", "modem", ...
	DeviceName string // e.g. "WAN Miniport (IKEv2)"
	Phonebook  string // the .pbk file the entry lives in
}

// RasConnections lists active RAS connections (RasEnumConnectionsW).
// If rasapi32.dll is missing (some stripped-down Windows editions) it returns none.
func RasConnections() ([]RasConn, error) {
	if procRasEnumConnections.Find() != nil {
		return nil, nil
	}
	entrySize := uint32(unsafe.Sizeof(rasConn{}))
	conns := make([]rasConn, 1)
	for attempt := 0; attempt < 3; attempt++ {
		conns[0].Size = entrySize // Windows checks this to know which layout we use
		bytes := entrySize * uint32(len(conns))
		var count uint32
		r, _, _ := procRasEnumConnections.Call(
			uintptr(unsafe.Pointer(&conns[0])),
			uintptr(unsafe.Pointer(&bytes)),
			uintptr(unsafe.Pointer(&count)),
		)
		if r == errorBufferTooSmall {
			conns = make([]rasConn, bytes/entrySize+1)
			continue
		}
		if r != 0 {
			return nil, &Error{Func: "RasEnumConnectionsW", Code: windows.Errno(r)}
		}
		out := make([]RasConn, 0, count)
		for _, c := range conns[:count] {
			out = append(out, RasConn{
				EntryName:  windows.UTF16ToString(c.EntryName[:]),
				DeviceType: windows.UTF16ToString(c.DeviceType[:]),
				DeviceName: windows.UTF16ToString(c.DeviceName[:]),
				Phonebook:  windows.UTF16ToString(c.Phonebook[:]),
			})
		}
		return out, nil
	}
	return nil, &Error{Func: "RasEnumConnectionsW", Code: errorBufferTooSmall}
}
