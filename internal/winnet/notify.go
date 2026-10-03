package winnet

import (
	"sync/atomic"

	"golang.org/x/sys/windows"
)

// changed is where the interface-change callback signals. atomic.Pointer lets the
// callback (on a Windows thread) and Watch/Stop (on ours) share it safely.
var changed atomic.Pointer[chan<- struct{}]

// interfaceCallback runs on a Windows thread whenever an IP interface is added,
// removed or changes state. It must never block, so it only does a non-blocking
// send. Created once: Go cannot free callbacks made with windows.NewCallback.
var interfaceCallback = windows.NewCallback(func(_ uintptr, _ uintptr, _ uint32) uintptr {
	if ch := changed.Load(); ch != nil {
		select {
		case *ch <- struct{}{}:
		default: // a wake-up is already pending; one is enough
		}
	}
	return 0
})

// InterfaceWatch is an active NotifyIpInterfaceChange registration.
type InterfaceWatch struct {
	handle windows.Handle
}

// WatchInterfaces sends on ch (without blocking) whenever an adapter is added,
// removed, or goes up or down. ch should have a buffer of 1. Only one watch at a time.
func WatchInterfaces(ch chan<- struct{}) (*InterfaceWatch, error) {
	changed.Store(&ch)
	w := &InterfaceWatch{}
	if err := windows.NotifyIpInterfaceChange(windows.AF_UNSPEC, interfaceCallback, nil, false, &w.handle); err != nil {
		changed.Store(nil)
		return nil, wrap("NotifyIpInterfaceChange", err)
	}
	return w, nil
}

// Stop cancels the watch.
func (w *InterfaceWatch) Stop() {
	if w == nil || w.handle == 0 {
		return
	}
	windows.CancelMibChangeNotify2(w.handle)
	w.handle = 0
	changed.Store(nil)
}
