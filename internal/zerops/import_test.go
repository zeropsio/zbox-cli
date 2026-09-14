package zerops

import (
	"strings"
	"testing"
)

// The YAML has to publish both hosts, generate the key the CLI reads back and hand it to the orchestrator under the name its config expects.
func TestRenderImport(t *testing.T) {
	out, err := RenderImport(ImportParams{ProjectName: "p", Hostname: "zbox", BinaryUrl: "https://x/zbox", ImageUrl: "https://x/base.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project:\n  name: p\n", "- port: 8080\n", "- port: 8081\n", "apiKey: <@generateRandomString(<32>)>", "start: /var/www/zbox run\n", "curl -fsSL -o /var/www/zbox.new https://x/zbox) || wget -qO /var/www/zbox.new https://x/zbox\n              - chmod +x /var/www/zbox.new && mv -f /var/www/zbox.new /var/www/zbox\n              - /var/www/zbox migrate\n              - ((command", "- sudo mkdir -p /var/lib/zbox && sudo chown zerops:zerops /var/lib/zbox\n", "https://x/base.tar.gz) || wget -qO- https://x/base.tar.gz) | docker load"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	out, _ = RenderImport(ImportParams{Hostname: "zbox", BinaryUrl: "b", ImageUrl: "i"})
	if strings.Contains(out, "project:") || !strings.HasPrefix(out, "services:") {
		t.Errorf("service-stack import must start at services:\n%s", out)
	}
}
