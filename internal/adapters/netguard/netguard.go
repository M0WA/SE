// Package netguard is a shared SSRF guard for outbound fetches to addresses
// this app doesn't fully control. Two policies: AllowedIP for the crawler
// (untrusted sites, so every private/reserved range is blocked), and the
// more permissive AllowedConfiguredEndpointIP for admin-configured chat/
// embedding endpoints (legitimately often private/loopback -- only
// link-local, e.g. cloud metadata IPs, plus multicast/unspecified are
// blocked there).
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// cgnatBlock is the IPv4 Carrier-Grade NAT range (RFC 6598) -- ISP-internal,
// not covered by net.IP.IsPrivate(), but just as internal as 10.0.0.0/8.
var cgnatBlock = mustParseCIDR("100.64.0.0/10")

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("netguard: invalid CIDR literal " + s + ": " + err.Error())
	}
	return n
}

// AllowedIP reports whether ip is safe for this crawler to connect to: not
// loopback, not link-local (this also covers the 169.254.169.254 cloud
// metadata address, which is link-local by definition), not a private/ULA
// range, not CGNAT, not unspecified/multicast.
func AllowedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	switch {
	case ip.IsLoopback(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast(),
		ip.IsUnspecified(),
		ip.IsPrivate():
		return false
	}
	if v4 := ip.To4(); v4 != nil && cgnatBlock.Contains(v4) {
		return false
	}
	return true
}

// AllowedConfiguredEndpointIP is AllowedIP's more permissive counterpart for
// an admin-configured endpoint (see package doc): loopback/private/CGNAT
// stay allowed since a self-hosted backend commonly lives there. Only
// link-local (covers cloud metadata IPs), other multicast, and unspecified
// addresses are blocked.
func AllowedConfiguredEndpointIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	switch {
	case ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast(),
		ip.IsUnspecified():
		return false
	}
	return true
}

// lookupIP is a package-level indirection over net.LookupIP purely so tests
// can substitute a fake resolver instead of depending on real DNS/network
// access -- production code never reassigns it.
var lookupIP = net.LookupIP

// blockedErr is returned by the dialer's Control hook, and detected by
// FriendlyDialError so callers can surface a clear "target is not allowed"
// message instead of a bare connection-refused-looking error.
type blockedErr struct{ addr string }

func (e *blockedErr) Error() string {
	return fmt.Sprintf("netguard: connection to %s is blocked (loopback/private/reserved address)", e.addr)
}

// SafeDialContext returns a DialContext rejecting any connection to a
// loopback/private/reserved IP (AllowedIP's policy), checked post-DNS
// pre-socket via the dialer's Control hook -- closes both a
// redirect-to-internal-URL bypass and DNS rebinding, unlike a pre-flight
// hostname check.
func SafeDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return safeDialContext(AllowedIP)
}

// ConfiguredEndpointDialContext is SafeDialContext's counterpart for an
// admin-configured integration endpoint, using AllowedConfiguredEndpointIP's
// more permissive policy instead.
func ConfiguredEndpointDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return safeDialContext(AllowedConfiguredEndpointIP)
}

func safeDialContext(allowed func(net.IP) bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: dialControl(allowed)}
	return dialer.DialContext
}

// dialControl builds a net.Dialer.Control hook for the given policy: it
// runs after DNS resolution but before the socket connects, given the exact
// address about to be connected to.
func dialControl(allowed func(net.IP) bool) func(_, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !allowed(ip) {
			return &blockedErr{addr: host}
		}
		return nil
	}
}

// Transport routes every dial (initial connection and redirect hops)
// through SafeDialContext, cloning http.DefaultTransport to keep its other
// tuning rather than reinventing it.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = SafeDialContext()
	return t
}

// ConfiguredEndpointTransport is Transport's counterpart for an
// admin-configured integration endpoint (httpchat/httpembed), using
// ConfiguredEndpointDialContext's more permissive policy instead.
func ConfiguredEndpointTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = ConfiguredEndpointDialContext()
	return t
}

// URLAllowed reports whether rawURL's scheme is http(s) and every IP its
// host resolves to is allowed under AllowedIP's policy. Used where a
// request never goes through an http.Transport (e.g. browserfetcher's own
// request interception, which has no dialer to hook) -- a resolve-then-check,
// so it carries the same DNS-rebinding TOCTOU gap as that handler, since
// neither Chromium nor Firefox expose a dial-level hook.
func URLAllowed(rawURL string) bool {
	return urlAllowed(rawURL, AllowedIP)
}

// ConfiguredEndpointURLAllowed is URLAllowed's counterpart using
// AllowedConfiguredEndpointIP's more permissive policy. httpchat/httpembed
// check this before building each request, a pre-flight barrier alongside
// ConfiguredEndpointTransport's connect-time check (belt-and-suspenders
// against the same TOCTOU gap, and recognizable to a static SSRF analyzer).
func ConfiguredEndpointURLAllowed(rawURL string) bool {
	return urlAllowed(rawURL, AllowedConfiguredEndpointIP)
}

func urlAllowed(rawURL string, allowed func(net.IP) bool) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return allowed(ip)
	}
	ips, err := lookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !allowed(ip) {
			return false
		}
	}
	return true
}
