package winsys

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wtsapi32                        = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
)

// WTS_INFO_CLASS values (wtsapi32.h).
const (
	wtsUserName   = 5
	wtsDomainName = 7

	noConsoleSession = 0xFFFFFFFF // WTSGetActiveConsoleSessionId when nobody is attached
)

// ConsoleUser returns the user logged on at the physical console, as "DOMAIN\user",
// or "" when nobody is logged on. A service runs as its own account, so this is how
// it finds out who is actually using the machine.
func ConsoleUser() string {
	id := windows.WTSGetActiveConsoleSessionId()
	if id == noConsoleSession {
		return ""
	}
	user := sessionString(id, wtsUserName)
	if user == "" {
		return "" // the console is at the logon screen
	}
	if domain := sessionString(id, wtsDomainName); domain != "" {
		return domain + `\` + user
	}
	return user
}

// sessionString wraps WTSQuerySessionInformationW for one text value. Windows
// allocates the buffer; we copy it and free it with WTSFreeMemory.
func sessionString(sessionID uint32, class uint32) string {
	var buf *uint16
	var size uint32
	r, _, _ := procWTSQuerySessionInformationW.Call(
		0, // WTS_CURRENT_SERVER_HANDLE: this machine
		uintptr(sessionID),
		uintptr(class),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 || buf == nil { // this API returns non-zero for success
		return ""
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf)
}
