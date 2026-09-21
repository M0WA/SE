// Package netguard is a shared SSRF guard for outbound fetches this app
// makes to addresses it does not fully control. It has two policies:
// AllowedIP for the crawler (crawled sites are untrusted and can redirect
// to, or request, an internal address like a cloud metadata service, so
// every private/reserved range is blocked), and the more permissive
// AllowedConfiguredEndpointIP for admin-configured integration endpoints
// (chat/embedding BaseURL) -- those legitimately, and commonly, point at a
// self-hosted backend on a private network or even loopback, so only
// classes of address with no legitimate self-hosted-integration use case
// (link-local, which covers every cloud provider's 169.254.169.254-style
// metadata service, plus multicast/unspecified) are blocked there.
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

// AllowedConfiguredEndpointIP reports whether ip is safe to connect to for
// an admin-configured integration endpoint (a ChatEndpoint or
// EmbeddingHTTPEndpoint BaseURL) -- see the package doc for why this is
// deliberately more permissive than AllowedIP: loopback and
// private/CGNAT ranges stay allowed since a self-hosted LLM/embedding
// backend commonly lives on exactly those. Only link-local (unicast and
// multicast -- this covers the 169.254.169.254 cloud metadata address),
// other multicast, and unspecified addresses are blocked.
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

// SafeDialContext returns a DialContext for an *http.Transport that rejects
// any connection to a loopback/private/reserved IP (AllowedIP's policy),
// checked via the dialer's Control hook against the exact address about to
// connect. That timing (post-DNS, pre-socket) closes both a
// redirect-to-internal-URL bypass and DNS rebinding, which a pre-flight
// hostname check can't.
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

// Transport is a ready-to-use *http.Transport that routes every dial
// (initial connection and every redirect hop) through SafeDialContext.
// Cloning http.DefaultTransport keeps its other tuning (idle connections,
// TLS handshake timeout, etc.) rather than reinventing it.
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
// request never goes through an http.Transport at all -- e.g. the
// headless-browser renderer's own request interception, which has no
// dialer of its own to hook.
//
// This is a resolve-then-check, not a connect-time check, so it carries the
// same DNS-rebinding TOCTOU gap as browserfetcher's Route handler -- neither
// Chromium nor Firefox expose a dial-level hook, so this is the strongest
// guard available at this layer.
func URLAllowed(rawURL string) bool {
	return urlAllowed(rawURL, AllowedIP)
}

// ConfiguredEndpointURLAllowed is URLAllowed's counterpart for an
// admin-configured integration endpoint, using
// AllowedConfiguredEndpointIP's more permissive policy instead. httpchat
// and httpembed check this immediately before building each request, as a
// pre-flight barrier alongside ConfiguredEndpointTransport's connect-time
// one (belt-and-suspenders against the exact same DNS-rebinding TOCTOU gap
// URLAllowed's own doc notes -- a Transport-level check alone closes it,
// but a pre-request check is also what a static SSRF analyzer can
// recognize as a guard on the URL actually used to build the request).
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
