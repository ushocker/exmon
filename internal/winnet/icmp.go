package winnet

import (
	"errors"
	"fmt"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ICMP status codes (IP_STATUS in ipexport.h).
const (
	StatusSuccess           = 0
	StatusDestNetUnreach    = 11002
	StatusDestHostUnreach   = 11003
	StatusDestPortUnreach   = 11005
	StatusTimedOut          = 11010
	StatusTTLExpiredTransit = 11013
	statusFirst             = 11000 // IP_STATUS_BASE
	statusLast              = 11050
)

type ipOptionInformation struct { // IP_OPTION_INFORMATION (16 bytes on 64-bit)
	TTL         uint8
	TOS         uint8
	Flags       uint8
	OptionsSize uint8
	OptionsData uintptr
}

type icmpEchoReply struct { // ICMP_ECHO_REPLY (40 bytes on 64-bit)
	Address       [4]byte // IPAddr: the host that answered
	Status        uint32
	RoundTripTime uint32 // ms
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

// Reply is the result of one ping.
type Reply struct {
	Status uint32        // StatusSuccess, StatusTimedOut, StatusTTLExpiredTransit, ...
	RTT    time.Duration // only meaningful when someone answered
	From   string        // who answered: the target, or a router when the TTL ran out
}

// OK reports whether the target itself answered.
func (r Reply) OK() bool { return r.Status == StatusSuccess }

// Answered reports whether anyone answered (the target, or a router on the way).
func (r Reply) Answered() bool { return r.Status != StatusTimedOut && r.From != "" }

// StatusText names a Reply.Status.
func StatusText(s uint32) string {
	switch s {
	case StatusSuccess:
		return "ok"
	case StatusTimedOut:
		return "timeout"
	case StatusTTLExpiredTransit:
		return "ttl_expired"
	case StatusDestHostUnreach:
		return "host_unreachable"
	case StatusDestNetUnreach:
		return "net_unreachable"
	case StatusDestPortUnreach:
		return "port_unreachable"
	}
	return fmt.Sprintf("status%d", s)
}

// Ping sends one ICMP echo request to an IPv4 address and waits up to timeout.
// ttl limits how many routers the packet may cross (0 means the default, 128);
// a path trace pings with TTL 1, 2, 3, ... and records who answers each time.
// Ping does not need administrator rights.
//
// A timeout is not an error: it comes back as Reply{Status: StatusTimedOut}.
func Ping(addr string, ttl int, timeout time.Duration) (Reply, error) {
	ip := net.ParseIP(addr).To4()
	if ip == nil {
		return Reply{}, fmt.Errorf("ping: %q is not an IPv4 address", addr)
	}

	h, _, err := procIcmpCreateFile.Call()
	if windows.Handle(h) == windows.InvalidHandle {
		return Reply{}, wrap("IcmpCreateFile", err)
	}
	defer procIcmpCloseHandle.Call(h)

	if ttl <= 0 {
		ttl = 128
	}
	opts := ipOptionInformation{TTL: uint8(min(ttl, 255))}
	payload := []byte("exmon-ping-exmon-ping-exmon-ping") // 32 bytes, like ping.exe
	// The reply holds one ICMP_ECHO_REPLY plus our echoed payload plus room for an
	// ICMP error message, per the IcmpSendEcho2 documentation.
	replyBuf := make([]byte, unsafe.Sizeof(icmpEchoReply{})+uintptr(len(payload))+8+64)

	// IPAddr is the 4 address bytes as they sit in memory.
	dest := *(*uint32)(unsafe.Pointer(&ip[0]))

	// Unlike the WLAN functions, IcmpSendEcho2 returns the number of replies; 0 means
	// "look at the error", which for timeouts and TTL expiry is an IP_STATUS code.
	n, _, callErr := procIcmpSendEcho2.Call(
		h,
		0, 0, 0, // no event, no APC: wait synchronously
		uintptr(dest),
		uintptr(unsafe.Pointer(&payload[0])),
		uintptr(len(payload)),
		uintptr(unsafe.Pointer(&opts)),
		uintptr(unsafe.Pointer(&replyBuf[0])),
		uintptr(len(replyBuf)),
		uintptr(timeout.Milliseconds()),
	)
	reply := (*icmpEchoReply)(unsafe.Pointer(&replyBuf[0]))

	if n == 0 {
		var errno windows.Errno
		if !errors.As(callErr, &errno) || uint32(errno) < statusFirst || uint32(errno) > statusLast {
			return Reply{}, wrap("IcmpSendEcho2", callErr)
		}
		// An IP_STATUS code: a normal outcome such as a timeout. Some Windows versions
		// still fill in who answered (e.g. the router whose TTL ran out).
		r := Reply{Status: uint32(errno)}
		if errno != StatusTimedOut && reply.Address != [4]byte{} {
			r.From = net.IP(reply.Address[:]).String()
			r.RTT = time.Duration(reply.RoundTripTime) * time.Millisecond
		}
		return r, nil
	}
	r := Reply{
		Status: reply.Status,
		RTT:    time.Duration(reply.RoundTripTime) * time.Millisecond,
	}
	if reply.Status != StatusTimedOut {
		r.From = net.IP(reply.Address[:]).String()
	}
	return r, nil
}
