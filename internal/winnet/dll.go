// Package winnet is a thin, typed wrapper over the Windows network APIs that exmon's
// NIC, probe and VPN monitors need: adapters and their counters, the route table,
// ICMP ping (with a TTL, for path traces), RAS VPN connections, interface-change
// notifications and the process list.
//
// Like internal/wlan, this is one of the only two packages that use unsafe/uintptr.
// Everything it returns is a plain Go value. Where golang.org/x/sys/windows already
// wraps a function, we call that; only the missing ones are bound here.
package winnet

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Functions that golang.org/x/sys/windows does not provide.
var (
	iphlpapi = windows.NewLazySystemDLL("iphlpapi.dll")
	rasapi32 = windows.NewLazySystemDLL("rasapi32.dll")

	procIcmpCreateFile     = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpCloseHandle    = iphlpapi.NewProc("IcmpCloseHandle")
	procIcmpSendEcho2      = iphlpapi.NewProc("IcmpSendEcho2")
	procRasEnumConnections = rasapi32.NewProc("RasEnumConnectionsW")
)

// Error is returned when a Windows network function fails.
type Error struct {
	Func string
	Code windows.Errno
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s failed: %v (Win32 error %d)", e.Func, e.Code, uint32(e.Code))
}

// wrap turns an error from x/sys/windows into an *Error naming the function.
func wrap(fn string, err error) error {
	if err == nil {
		return nil
	}
	if errno, ok := err.(windows.Errno); ok {
		return &Error{Func: fn, Code: errno}
	}
	return fmt.Errorf("%s failed: %w", fn, err)
}
