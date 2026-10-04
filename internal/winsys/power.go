package winsys

import (
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	powrprof                                     = windows.NewLazySystemDLL("powrprof.dll")
	procPowerRegisterSuspendResumeNotification   = powrprof.NewProc("PowerRegisterSuspendResumeNotification")
	procPowerUnregisterSuspendResumeNotification = powrprof.NewProc("PowerUnregisterSuspendResumeNotification")
)

// Power broadcast types (winuser.h) and the registration flag.
const (
	deviceNotifyCallback  = 2    // DEVICE_NOTIFY_CALLBACK
	pbtAPMSuspend         = 0x4  // PBT_APMSUSPEND: about to sleep or hibernate
	pbtAPMResumeSuspend   = 0x7  // PBT_APMRESUMESUSPEND: resumed, a user is present
	pbtAPMResumeAutomatic = 0x12 // PBT_APMRESUMEAUTOMATIC: resumed (always sent)
)

// PowerEvent is a sleep or wake notification from Windows.
type PowerEvent int

const (
	PowerSuspend PowerEvent = iota + 1 // the machine is going to sleep or hibernate
	PowerResume                        // the machine has woken up
)

func (p PowerEvent) String() string {
	switch p {
	case PowerSuspend:
		return "suspend"
	case PowerResume:
		return "resume"
	}
	return "unknown"
}

type deviceNotifySubscribeParameters struct { // DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS
	Callback uintptr
	Context  uintptr
}

// powerTarget is where the callback sends events. atomic.Pointer lets the callback
// (on a Windows thread) and WatchPower/Stop (on ours) share it safely.
var powerTarget atomic.Pointer[chan<- PowerEvent]

// powerCallback runs on a Windows thread for each power broadcast. It must not
// block, so it only does a non-blocking send. Created once: Go can't free callbacks.
var powerCallback = windows.NewCallback(func(_ uintptr, typ uintptr, _ uintptr) uintptr {
	ch := powerTarget.Load()
	if ch == nil {
		return 0
	}
	var ev PowerEvent
	switch typ {
	case pbtAPMSuspend:
		ev = PowerSuspend
	case pbtAPMResumeAutomatic, pbtAPMResumeSuspend:
		ev = PowerResume // both may arrive for one wake; the receiver treats repeats as one
	default:
		return 0
	}
	select {
	case *ch <- ev:
	default:
	}
	return 0
})

// powerParams must stay alive for as long as the registration exists, because
// Windows keeps the pointer; a package-level variable always stays alive.
var powerParams = deviceNotifySubscribeParameters{Callback: powerCallback}

// PowerWatch is an active suspend/resume registration.
type PowerWatch struct {
	handle uintptr
}

// WatchPower sends PowerSuspend and PowerResume on ch (without blocking) when the
// machine goes to sleep, hibernates or wakes. It works in a console and in a
// service. Only one watch at a time; ch should be buffered.
func WatchPower(ch chan<- PowerEvent) (*PowerWatch, error) {
	if err := procPowerRegisterSuspendResumeNotification.Find(); err != nil {
		return nil, err // Windows older than 8
	}
	powerTarget.Store(&ch)
	w := &PowerWatch{}
	// This API returns a Win32 error code directly (0 = success).
	r, _, _ := procPowerRegisterSuspendResumeNotification.Call(
		deviceNotifyCallback,
		uintptr(unsafe.Pointer(&powerParams)),
		uintptr(unsafe.Pointer(&w.handle)),
	)
	if r != 0 {
		powerTarget.Store(nil)
		return nil, windows.Errno(r)
	}
	return w, nil
}

// Stop cancels the watch.
func (w *PowerWatch) Stop() {
	if w == nil || w.handle == 0 {
		return
	}
	procPowerUnregisterSuspendResumeNotification.Call(w.handle)
	w.handle = 0
	powerTarget.Store(nil)
}
