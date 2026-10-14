package zerops

import "testing"

func TestSubdomainHosts(t *testing.T) {
	api, ingress := SubdomainHosts("https://zbox-f-8080.app-futon.zerops.dev\nhttps://zbox-f-8081.app-futon.zerops.dev\n")
	if api != "zbox-f-8080.app-futon.zerops.dev" || ingress != "zbox-f-8081.app-futon.zerops.dev" {
		t.Errorf("got %q %q", api, ingress)
	}
	if api, ingress := SubdomainHosts(""); api != "" || ingress != "" {
		t.Errorf("empty: got %q %q", api, ingress)
	}
	if api, _ := SubdomainHosts("zbox-f-8080.app-futon.zerops.dev"); api != "zbox-f-8080.app-futon.zerops.dev" {
		t.Errorf("bare host: got %q", api)
	}
}
