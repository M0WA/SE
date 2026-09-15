package netguard

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestAllowedIP(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{"loopback v4", "127.0.0.1", false},
		{"loopback v6", "::1", false},
		{"link-local metadata", "169.254.169.254", false},
		{"link-local v4 other", "169.254.1.1", false},
		{"private 10/8", "10.0.0.5", false},
		{"private 172.16/12", "172.16.5.5", false},
		{"private 192.168/16", "192.168.1.1", false},
		{"cgnat", "100.64.0.1", false},
		{"cgnat upper bound", "100.127.255.254", false},
		{"unspecified v4", "0.0.0.0", false},
		{"unspecified v6", "::", false},
		{"multicast", "224.0.0.1", false},
		{"ipv6 ULA", "fc00::1", false},
		{"public v4", "8.8.8.8", true},
		{"public v6", "2001:4860:4860::8888", true},
		{"just outside cgnat below", "100.63.255.255", true},
		{"just outside cgnat above", "100.128.0.0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("test setup: %q did not parse as an IP", tc.ip)
			}
			if got := AllowedIP(ip); got != tc.want {
				t.Errorf("AllowedIP(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestAllowedIP_Nil(t *testing.T) {
	if AllowedIP(nil) {
		t.Error("expected nil IP to be disallowed")
	}
}

func TestURLAllowed_LiteralIP(t *testing.T) {
	if URLAllowed("http://127.0.0.1/admin") {
		t.Error("expected loopback literal IP URL to be disallowed")
	}
	if URLAllowed("http://169.254.169.254/latest/meta-data/") {
		t.Error("expected cloud metadata literal IP URL to be disallowed")
	}
	if !URLAllowed("http://93.184.216.34/") {
		t.Error("expected public literal IP URL to be allowed")
	}
}

func TestURLAllowed_RejectsNonHTTPScheme(t *testing.T) {
	if URLAllowed("file:///etc/passwd") {
		t.Error("expected non-http(s) scheme to be disallowed")
	}
	if URLAllowed("ftp://example.com/") {
		t.Error("expected ftp scheme to be disallowed")
	}
}

func TestURLAllowed_RejectsUnparseable(t *testing.T) {
	if URLAllowed("http://[::1") {
		t.Error("expected unparseable URL to be disallowed")
	}
}

func TestURLAllowed_RejectsEmptyHost(t *testing.T) {
	if URLAllowed("http:///path") {
		t.Error("expected empty host to be disallowed")
	}
}

func TestURLAllowed_ResolvesHostname(t *testing.T) {
	orig := lookupIP
	defer func() { lookupIP = orig }()

	lookupIP = func(host string) ([]net.IP, error) {
		if host == "internal.example" {
			return []net.IP{net.ParseIP("10.0.0.1")}, nil
		}
		if host == "public.example" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		if host == "mixed.example" {
			// A hostname that resolves to both a public and a private
			// address must still be rejected -- allowing it would let an
			// attacker with control over one A record entry smuggle a
			// second, internal-only address past the check.
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.1")}, nil
		}
		return nil, errors.New("no such host")
	}

	if URLAllowed("http://internal.example/") {
		t.Error("expected hostname resolving to a private IP to be disallowed")
	}
	if !URLAllowed("http://public.example/") {
		t.Error("expected hostname resolving to a public IP to be allowed")
	}
	if URLAllowed("http://mixed.example/") {
		t.Error("expected hostname resolving to any private IP to be disallowed")
	}
	if URLAllowed("http://nonexistent.example/") {
		t.Error("expected a resolution failure to be disallowed")
	}
}

func TestMustParseCIDR_PanicsOnInvalidCIDR(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected mustParseCIDR to panic on an invalid CIDR literal")
		}
	}()
	mustParseCIDR("not-a-cidr")
}

func TestDialControl_RejectsUnsplittableAddress(t *testing.T) {
	if err := dialControl("tcp", "no-port-here", nil); err == nil {
		t.Fatal("expected an address with no port to be rejected")
	}
}

func TestDialControl_AllowsPublicIP(t *testing.T) {
	if err := dialControl("tcp", "93.184.216.34:80", nil); err != nil {
		t.Errorf("expected a public IP to be allowed, got: %v", err)
	}
}

func TestDialControl_RejectsNonIPHost(t *testing.T) {
	// The Control hook is only ever invoked by net.Dialer with an already-
	// resolved numeric address, so this shouldn't happen in practice --
	// but if it ever did (e.g. a future net package change), a host that
	// isn't a literal IP must still be rejected rather than let through.
	if err := dialControl("tcp", "example.com:80", nil); err == nil {
		t.Fatal("expected a non-IP host to be rejected")
	}
}

func TestSafeDialContext_BlocksPrivateTarget(t *testing.T) {
	dial := SafeDialContext()
	_, err := dial(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected dial to a loopback address to be blocked")
	}
	if !strings.Contains(err.Error(), "netguard") {
		t.Errorf("expected a netguard error, got: %v", err)
	}
}

func TestSafeDialContext_AllowsPublicTarget(t *testing.T) {
	// A public-looking address must clear the Control hook itself -- this
	// sandboxed test environment has no real route to the internet, so the
	// dial is expected to fail for network reasons, but NOT with a
	// netguard-blocked error; that distinguishes "the Control hook
	// rejected this address" from "the address was allowed, but the dial
	// itself failed for an unrelated reason" (which a zero-timeout context
	// guarantees deterministically, without depending on real
	// connectivity).
	dial := SafeDialContext()
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_, err := dial(ctx, "tcp", "93.184.216.34:80")
	if err == nil {
		t.Fatal("expected the zero-timeout context to fail the dial")
	}
	if strings.Contains(err.Error(), "netguard") {
		t.Errorf("expected a public address to pass the netguard check (fail for a different reason), got: %v", err)
	}
}

func TestTransport_UsesSafeDialContext(t *testing.T) {
	tr := Transport()
	if tr.DialContext == nil {
		t.Fatal("expected Transport to set DialContext")
	}
	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), "netguard") {
		t.Errorf("expected Transport's DialContext to block a loopback target, got: %v", err)
	}
}
