package zerops

import (
	"strings"
	"testing"
)

// The YAML has to publish both hosts, carry no key of its own (the controller generates ZBOX_WORKSPACE_API_KEY), pick the Firecracker substrate and migrate before the mounted orchestrator starts, without sudo.
func TestRenderImport(t *testing.T) {
	out, err := RenderImport(ImportParams{ProjectName: "p", Hostname: "zbox"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project:\n  name: p\n", "type: zbox@1\n", "base: zbox@1\n", "- port: 8080\n", "- port: 8081\n", "ZBOX_SUBSTRATE_KIND: FIRECRACKER\n", "minContainers: 1\n    maxContainers: 1\n    verticalAutoscaling:\n      cpuMode: DEDICATED\n      minCpu: 2\n      maxCpu: 2\n      minRam: 8\n      maxRam: 8\n      minDisk: 50\n      maxDisk: 50\n", "initCommands:\n              - /opt/zerops/bin/zerops-zbox-orchestrator migrate\n            start: /opt/zerops/bin/zerops-zbox-orchestrator run\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, stale := range []string{"docker", "mkdir", "sudo", "envSecrets", "apiKey"} {
		if strings.Contains(out, stale) {
			t.Errorf("stale %q in:\n%s", stale, out)
		}
	}
	out, _ = RenderImport(ImportParams{Hostname: "zbox"})
	if strings.Contains(out, "project:") || !strings.HasPrefix(out, "services:") {
		t.Errorf("service-stack import must start at services:\n%s", out)
	}
}
