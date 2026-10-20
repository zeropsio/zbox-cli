package zerops

import "testing"

func TestSubdomainHosts(t *testing.T) {
	api, ingress := SubdomainHosts("https://zbox-f-8080.app-prg1.zerops.io\nhttps://zbox-f-8081.app-prg1.zerops.io\n")
	if api != "zbox-f-8080.app-prg1.zerops.io" || ingress != "zbox-f-8081.app-prg1.zerops.io" {
		t.Errorf("got %q %q", api, ingress)
	}
	if api, ingress := SubdomainHosts(""); api != "" || ingress != "" {
		t.Errorf("empty: got %q %q", api, ingress)
	}
	if api, _ := SubdomainHosts("zbox-f-8080.app-prg1.zerops.io"); api != "zbox-f-8080.app-prg1.zerops.io" {
		t.Errorf("bare host: got %q", api)
	}
}
