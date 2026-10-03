package winnet

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Interface types we care about (IF_TYPE_* in ipifcons.h).
const (
	TypeEthernet = windows.IF_TYPE_ETHERNET_CSMACD
	TypeWiFi     = windows.IF_TYPE_IEEE80211
	TypeLoopback = windows.IF_TYPE_SOFTWARE_LOOPBACK
	TypeTunnel   = windows.IF_TYPE_TUNNEL
	TypePPP      = windows.IF_TYPE_PPP
	TypeVirtual  = 53 // IF_TYPE_PROP_VIRTUAL: used by many VPN clients (Tailscale, WireGuard)
)

// Adapter is one network interface.
type Adapter struct {
	Name        string // friendly name, e.g. "Wi-Fi"
	Description string // driver description, e.g. "Intel(R) Ethernet Connection"
	Type        uint32 // IF_TYPE_*; see TypeName
	Up          bool
	SpeedMbps   int64 // transmit link speed
	MAC         string
	IPv4        []string
	Gateways    []string // IPv4 only
	DNSServers  []string // IPv4 only
	DNSSuffix   string
	Metric      uint32 // IPv4 interface metric, added to a route's metric to rank routes
	LUID        uint64
	IfIndex     uint32
}

// TypeName names an IF_TYPE_* value.
func TypeName(t uint32) string {
	switch t {
	case TypeEthernet:
		return "ethernet"
	case TypeWiFi:
		return "wifi"
	case TypeLoopback:
		return "loopback"
	case TypeTunnel:
		return "tunnel"
	case TypePPP:
		return "ppp"
	case TypeVirtual:
		return "virtual"
	}
	return fmt.Sprintf("type%d", t)
}

// Adapters lists the network interfaces (GetAdaptersAddresses).
func Adapters() ([]Adapter, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST

	// Windows tells us how big a buffer it needs; try, grow, try again.
	size := uint32(16 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		// []uint64 rather than []byte so the buffer is 8-byte aligned for the structs.
		buf := make([]uint64, size/8+1)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue // size now holds the required length
		}
		if err != nil {
			return nil, wrap("GetAdaptersAddresses", err)
		}
		var out []Adapter
		for a := first; a != nil; a = a.Next { // a linked list: follow Next until nil
			out = append(out, adapterFrom(a))
		}
		return out, nil
	}
	return nil, errors.New("GetAdaptersAddresses: buffer kept growing")
}

func adapterFrom(a *windows.IpAdapterAddresses) Adapter {
	ad := Adapter{
		Name:        windows.UTF16PtrToString(a.FriendlyName),
		Description: windows.UTF16PtrToString(a.Description),
		DNSSuffix:   windows.UTF16PtrToString(a.DnsSuffix),
		Type:        a.IfType,
		Up:          a.OperStatus == windows.IfOperStatusUp,
		SpeedMbps:   int64(a.TransmitLinkSpeed / 1_000_000),
		Metric:      a.Ipv4Metric,
		LUID:        a.Luid,
		IfIndex:     a.IfIndex,
	}
	if a.TransmitLinkSpeed == ^uint64(0) { // "unknown"
		ad.SpeedMbps = 0
	}
	if n := a.PhysicalAddressLength; n > 0 && n <= uint32(len(a.PhysicalAddress)) {
		ad.MAC = strings.ToUpper(net.HardwareAddr(a.PhysicalAddress[:n]).String())
	}
	for u := a.FirstUnicastAddress; u != nil; u = u.Next {
		if ip := ipv4(u.Address); ip != "" {
			ad.IPv4 = append(ad.IPv4, ip)
		}
	}
	for g := a.FirstGatewayAddress; g != nil; g = g.Next {
		if ip := ipv4(g.Address); ip != "" {
			ad.Gateways = append(ad.Gateways, ip)
		}
	}
	for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
		if ip := ipv4(d.Address); ip != "" {
			ad.DNSServers = append(ad.DNSServers, ip)
		}
	}
	return ad
}

// ipv4 returns the address as text if it is IPv4, else "".
func ipv4(s windows.SocketAddress) string {
	if s.Sockaddr == nil {
		return ""
	}
	if ip := s.IP().To4(); ip != nil {
		return ip.String()
	}
	return ""
}

// IfCounters are an interface's cumulative traffic counters.
type IfCounters struct {
	InOctets, OutOctets     uint64
	InPackets, OutPackets   uint64 // unicast
	InErrors, OutErrors     uint64
	InDiscards, OutDiscards uint64
}

// Counters reads an interface's counters by LUID (GetIfEntry2Ex).
func Counters(luid uint64) (IfCounters, error) {
	row := windows.MibIfRow2{InterfaceLuid: luid}
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row); err != nil {
		return IfCounters{}, wrap("GetIfEntry2Ex", err)
	}
	return IfCounters{
		InOctets: row.InOctets, OutOctets: row.OutOctets,
		InPackets: row.InUcastPkts, OutPackets: row.OutUcastPkts,
		InErrors: row.InErrors, OutErrors: row.OutErrors,
		InDiscards: row.InDiscards, OutDiscards: row.OutDiscards,
	}, nil
}
