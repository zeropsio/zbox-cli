package zerops

import (
	"net/url"
	"strings"
)

// SubdomainHosts takes the hosts out of the service env zeropsSubdomain: the platform writes one URL per published port, API first, ingress second
// (zbox design §6.1), so a service without subdomain access has none.
func SubdomainHosts(value string) (apiHost, ingressHost string) {
	var hosts []string
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if parsed, err := url.Parse(line); err == nil && parsed.Host != "" {
			line = parsed.Host
		}
		hosts = append(hosts, line)
	}
	hosts = append(hosts, "", "")
	return hosts[0], hosts[1]
}
