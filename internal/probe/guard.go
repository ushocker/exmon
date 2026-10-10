package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// Addresses probes never contact, whatever the config says. A probe target
// comes from the config, which may be delivered from the cloud; these addresses
// would let a bad config reach things on or inside the PC instead of the network:
//
//   - 169.254.0.0/16 and fe80::/10: link-local, including cloud metadata services
//     at 169.254.169.254 that hand out credentials on cloud VMs;
//   - 168.63.129.16: Azure's host "wire server";
//   - unspecified and multicast addresses.
//
// Loopback (the PC itself) is allowed: probing a local service is a legitimate
// check, and it doesn't reach anything beyond the PC.
var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{"169.254.0.0/16", "fe80::/10", "168.63.129.16/32", "0.0.0.0/8", "::/128", "224.0.0.0/4", "ff00::/8"} {
		_, n, _ := net.ParseCIDR(cidr)
		out = append(out, n)
	}
	return out
}()

// errBlocked is returned for a target in blockedNets.
var errBlocked = errors.New("address is not allowed as a probe target (link-local, cloud metadata or multicast)")

// blocked reports whether ip may not be probed.
func blocked(ip net.IP) bool {
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// checkTarget fails if ip may not be probed.
func checkTarget(ip net.IP) error {
	if blocked(ip) {
		return fmt.Errorf("%s: %w", ip, errBlocked)
	}
	return nil
}

// guardedDialContext checks the address the HTTP client actually connects to,
// after DNS, so a name that resolves to a blocked address is refused too.
func guardedDialContext(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		lookup := "ip"
		if network == "tcp4" {
			lookup = "ip4"
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, lookup, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if err := checkTarget(ip); err != nil {
				return nil, err
			}
		}
		// Dial the address we checked, not the name, so a second lookup can't
		// return something different.
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

// maxRedirects is the most redirects an HTTP probe follows.
const maxRedirects = 5

// checkRedirect stops after maxRedirects (Go's default is 10).
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	return nil
}
