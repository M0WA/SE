package dockersandbox

import (
	"fmt"
	"os"
	"strings"
)

// DetectHostDNS returns the DNS nameserver IPs this host itself actually
// uses to resolve names -- for populating Limits.DNS (see cmd/mcp-sandbox's
// -host-dns flag) so a network-enabled sandbox resolves names the same way
// the host does, rather than through Docker's own embedded DNS server
// (127.0.0.11, forwarding to whatever the Docker daemon's own network
// config happens to be, which can differ from the host's -- e.g. a
// corporate/VPN DNS setup the host resolves through but the Docker daemon
// itself was never told about).
//
// Prefers /run/systemd/resolve/resolv.conf over /etc/resolv.conf when
// present: on a systemd-resolved host (the default on Debian/Ubuntu, this
// project's own target deployment OS), /etc/resolv.conf points at
// 127.0.0.53, a stub resolver that only listens on the HOST's own
// loopback -- unreachable from inside a container's own network
// namespace, so passing it straight through as --dns would break
// resolution entirely rather than fix it. /run/systemd/resolve/resolv.conf
// is systemd-resolved's own record of the REAL upstream nameservers it
// forwards to, which a container can actually reach.
func DetectHostDNS() ([]string, error) {
	return detectHostDNSFromPaths([]string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"})
}

// detectHostDNSFromPaths is DetectHostDNS's own logic against an explicit,
// test-substitutable path list, tried in order -- factored out so a test
// can exercise the real parsing/fallback behavior against temp files
// instead of the host's own actual DNS config.
func detectHostDNSFromPaths(paths []string) ([]string, error) {
	for _, path := range paths {
		servers, err := parseNameservers(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("dockersandbox: reading %s: %w", path, err)
		}
		if len(servers) > 0 {
			return servers, nil
		}
	}
	return nil, fmt.Errorf("dockersandbox: no nameserver line found in %s", strings.Join(paths, " or "))
}

// parseNameservers extracts every "nameserver <ip>" line's IP from a
// resolv.conf-formatted file at path, in file order.
func parseNameservers(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" {
			servers = append(servers, fields[1])
		}
	}
	return servers, nil
}
