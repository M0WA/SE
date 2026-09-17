// Package netguard is a shared SSRF guard for every outbound fetch this
// crawler makes -- crawled sites are untrusted and can redirect to (or, via
// JS when rendering is enabled, request) an internal address like a cloud
// metadata service. Nothing else in the codebase stops that.
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

// cgnatBlock is the IPv4 Carrier-Grade NAT range (RFC 6598) -- reserved for
// ISP-internal use, not covered by net.IP.IsPrivate() (which only knows
// RFC1918 + the IPv6 ULA range), but just as much an internal-network
// address as 10.0.0.0/8 from this app's point of view.
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

// SafeDialContext returns a DialContext for an *http.Transport that rejects
// any connection to a loopback/private/reserved IP, checked via the
// dialer's Control hook against the exact address about to connect. That
// timing (post-DNS, pre-socket) closes both a redirect-to-internal-URL
// bypass and DNS rebinding, which a pre-flight hostname check can't.
func SafeDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: dialControl}
	return dialer.DialContext
}

// dialControl is a net.Dialer.Control hook: it runs after DNS resolution
// but before the socket connects, given the exact address about to be
// connected to.
func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !AllowedIP(ip) {
		return &blockedErr{addr: host}
	}
	return nil
}

// Transport is a ready-to-use *http.Transport that routes every dial
// (initial connection and every redirect hop) through SafeDialContext.
// Cloning http.DefaultTransport keeps its other tuning (idle connections,
// TLS handshake timeout, etc.) rather than reinventing it.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = SafeDialContext()
	return t
}

// URLAllowed reports whether rawURL's scheme is http(s) and every IP its
// host resolves to is allowed. Used where a request never goes through an
// http.Transport at all -- e.g. the headless-browser renderer's own request
// interception, which has no dialer of its own to hook.
//
// This is a resolve-then-check, not a connect-time check, so it carries the
// same DNS-rebinding TOCTOU gap as browserfetcher's Route handler -- neither
// Chromium nor Firefox expose a dial-level hook, so this is the strongest
// guard available at this layer.
func URLAllowed(rawURL string) bool {
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
		return AllowedIP(ip)
	}
	ips, err := lookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !AllowedIP(ip) {
			return false
		}
	}
	return true
}
