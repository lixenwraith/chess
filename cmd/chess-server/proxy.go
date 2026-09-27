package main

import (
	"fmt"
	"net/netip"
	"strings"
)

// parseTrustedProxies validates a comma-separated list of IPs and CIDRs.
// Fiber skips entries it cannot parse, which would silently disable trust.
func parseTrustedProxies(value string) ([]string, error) {
	var proxies []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, err := netip.ParsePrefix(entry); err != nil {
				return nil, fmt.Errorf("%q is not a valid CIDR", entry)
			}
		} else if _, err := netip.ParseAddr(entry); err != nil {
			return nil, fmt.Errorf("%q is not a valid IP address", entry)
		}
		proxies = append(proxies, entry)
	}
	return proxies, nil
}
