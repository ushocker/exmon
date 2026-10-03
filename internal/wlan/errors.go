package wlan

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// Win32 error codes that mean the handle is dead and must be reopened.
const (
	errInvalidHandle    = windows.Errno(6)    // ERROR_INVALID_HANDLE
	errServiceNotActive = windows.Errno(1062) // ERROR_SERVICE_NOT_ACTIVE (WlanSvc stopped or restarting)
)

// Error is returned when a wlanapi function returns a non-zero Win32 error code.
type Error struct {
	Func string
	Code windows.Errno
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s failed: %v (Win32 error %d)", e.Func, e.Code, uint32(e.Code))
}

// check turns a wlanapi return code into an error (nil for success).
func check(fn string, r uintptr) error {
	if r == 0 {
		return nil
	}
	return &Error{Func: fn, Code: windows.Errno(r)}
}

// IsStale reports whether err means the wlanapi handle no longer works (WlanSvc was
// restarted, for example) and must be closed and reopened.
func IsStale(err error) bool {
	// errors.As looks through wrapped errors for one of type *Error and, if found,
	// stores it in e.
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code == errInvalidHandle || e.Code == errServiceNotActive
}
