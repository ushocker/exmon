package winnet

import (
	"net"

	"golang.org/x/sys/windows"
)

const (
	dnsQueryBypassCache = 0x00000008 // DNS_QUERY_BYPASS_CACHE
	dnsFreeRecordList   = 1          // DnsFreeRecordList
)

// LookupA resolves a name's IPv4 addresses with a real query to the configured DNS
// servers, skipping the Windows DNS cache, so the time it takes is the network's
// time (Go's normal resolver would often answer from the cache in ~0 ms).
func LookupA(name string) ([]string, error) {
	var rec *windows.DNSRecord
	if err := windows.DnsQuery(name, windows.DNS_TYPE_A, dnsQueryBypassCache, nil, &rec, nil); err != nil {
		return nil, wrap("DnsQuery", err)
	}
	defer windows.DnsRecordListFree(rec, dnsFreeRecordList)

	var out []string
	for r := rec; r != nil; r = r.Next {
		if r.Type == windows.DNS_TYPE_A { // a CNAME chain also comes back; keep only A records
			out = append(out, net.IP(r.Data[:4]).String())
		}
	}
	return out, nil
}
