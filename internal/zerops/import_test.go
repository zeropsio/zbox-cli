package zerops

import (
	"strings"
	"testing"
)

// The YAML has to publish both hosts, generate the key the CLI reads back, migrate before the mounted orchestrator starts and load the default image only when it is missing.
func TestRenderImport(t *testing.T) {
	out, err := RenderImport(ImportParams{ProjectName: "p", Hostname: "zbox", ImageUrl: "https://x/base.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project:\n  name: p\n", "- port: 8080\n", "- port: 8081\n", "apiKey: <@generateRandomString(<32>)>", "start: /opt/zerops/bin/zerops-zbox-orchestrator run\n", "- sudo mkdir -p /var/lib/zbox && sudo chown zerops:zerops /var/lib/zbox\n              - /opt/zerops/bin/zerops-zbox-orchestrator migrate\n              - docker image inspect zbox-base:latest >/dev/null 2>&1 || ((command -v curl >/dev/null && curl -fsSL https://x/base.tar.gz) || wget -qO- https://x/base.tar.gz) | docker load\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out, _ = RenderImport(ImportParams{Hostname: "zbox", ImageUrl: "i"})
	if strings.Contains(out, "project:") || !strings.HasPrefix(out, "services:") {
		t.Errorf("service-stack import must start at services:\n%s", out)
	}
}
