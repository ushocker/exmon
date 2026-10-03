package winnet

import (
	"net"
	"sort"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Route is one IPv4 default route (0.0.0.0/0).
type Route struct {
	IfIndex uint32
	LUID    uint64
	NextHop string // "0.0.0.0" for on-link routes (typical for VPN tunnels)
	Metric  uint32 // route metric only; add the adapter's Metric to compare routes
}

// DefaultRoutes lists the IPv4 default routes (GetIpForwardTable2).
func DefaultRoutes() ([]Route, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil {
		return nil, wrap("GetIpForwardTable2", err)
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	var out []Route
	for _, r := range table.Rows() {
		if r.DestinationPrefix.PrefixLength != 0 || r.DestinationPrefix.Prefix.Family != windows.AF_INET {
			continue
		}
		out = append(out, Route{
			IfIndex: r.InterfaceIndex,
			LUID:    r.InterfaceLuid,
			NextHop: inet4(&r.NextHop),
			Metric:  r.Metric,
		})
	}
	return out, nil
}

// BestDefaultRoute picks the default route Windows would use: lowest route metric
// plus interface metric, among adapters that are up. ok is false if there is none.
func BestDefaultRoute(routes []Route, adapters []Adapter) (r Route, a Adapter, ok bool) {
	byIndex := map[uint32]Adapter{}
	for _, ad := range adapters {
		byIndex[ad.IfIndex] = ad
	}
	type cand struct {
		r      Route
		a      Adapter
		metric uint32
	}
	var cands []cand
	for _, rt := range routes {
		if ad, found := byIndex[rt.IfIndex]; found && ad.Up {
			cands = append(cands, cand{rt, ad, rt.Metric + ad.Metric})
		}
	}
	if len(cands) == 0 {
		return Route{}, Adapter{}, false
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].metric < cands[j].metric })
	return cands[0].r, cands[0].a, true
}

// inet4 reads an IPv4 address from a SOCKADDR_INET union.
func inet4(s *windows.RawSockaddrInet) string {
	if s.Family != windows.AF_INET {
		return ""
	}
	v4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(s))
	return net.IP(v4.Addr[:]).String()
}
