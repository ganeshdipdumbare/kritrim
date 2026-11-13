// Package netguard decides which network addresses untrusted requests may
// reach. The check runs at dial time, on the IP actually connected to, so a
// DNS answer that changes between check and connect (rebinding) cannot reach
// a private address.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// blocked lists ranges that are never public: private, loopback, link-local,
// CGNAT, benchmarking, documentation, reserved, multicast and translation
// prefixes. IPv4-mapped and NAT64/6to4 addresses are unwrapped first.
var blocked = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "100::/64", "2001::/23", "2001:db8::/32", "fc00::/7",
	"fe80::/10", "fec0::/10", "ff00::/8",
)

var (
	nat64   = netip.MustParsePrefix("64:ff9b::/96")
	nat64l  = netip.MustParsePrefix("64:ff9b:1::/48")
	sixTo4  = netip.MustParsePrefix("2002::/16")
	teredo  = netip.MustParsePrefix("2001::/32")
	errDeny = errors.New("address is not public")
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// Public reports whether ip is a globally routable unicast address.
func Public(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	if ip.Is6() {
		switch {
		case nat64.Contains(ip), nat64l.Contains(ip):
			b := ip.As16()
			return Public(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case sixTo4.Contains(ip):
			b := ip.As16()
			return Public(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		case teredo.Contains(ip):
			return false
		}
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// CheckHost resolves host and fails if any answer is not public. It is an
// early, friendly error; the dialer is the enforcement point.
func CheckHost(ctx context.Context, host string) error {
	ips, err := lookup(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if !Public(ip) {
			return fmt.Errorf("host %q resolves to a private address", host)
		}
	}
	return nil
}

func lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %q: no addresses", host)
	}
	return addrs, nil
}

// Trusted reports whether a dial to host may skip the public-address check
// (an exact allowlist entry or a configured proxy).
type Trusted func(host string) bool

// DialContext returns a dial function that resolves the target itself,
// rejects non-public answers unless trusted(host), and connects to the
// validated IP.
func DialContext(trusted Trusted) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if trusted != nil && trusted(strings.ToLower(host)) {
			return d.DialContext(ctx, network, addr)
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !Public(ip) {
				return nil, fmt.Errorf("dial %s: %w (%s)", host, errDeny, ip)
			}
		}
		var last error
		for _, ip := range ips {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}
}

// Client returns a copy of base whose transport dials through DialContext.
// Keep-alives are disabled so a connection validated for one request is never
// reused under different rules. A base client with a custom RoundTripper
// that is not an *http.Transport is returned unchanged (ok == false); the
// caller then relies on CheckHost only.
func Client(base *http.Client, trusted Trusted) (c *http.Client, ok bool) {
	if base == nil {
		base = &http.Client{Timeout: 2 * time.Minute}
	}
	cp := *base
	var tr *http.Transport
	switch t := base.Transport.(type) {
	case nil:
		tr = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		tr = t.Clone()
	default:
		return &cp, false
	}
	// A proxy would hide the target IP from the dialer, so guarded
	// requests always connect directly.
	tr.Proxy = nil
	tr.DisableKeepAlives = true
	tr.DialContext = DialContext(trusted)
	tr.DialTLSContext = nil
	cp.Transport = tr
	return &cp, true
}
