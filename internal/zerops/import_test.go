package zerops

import (
	"strings"
	"testing"
)

// The YAML has to publish both hosts, carry no key of its own (the controller generates ZBOX_WORKSPACE_API_KEY), set no substrate env (Firecracker is the only one) and run migrate, host prepare, base ensure and the template lines in that order before the mounted orchestrator starts, without sudo.
func TestRenderImport(t *testing.T) {
	out, err := RenderImport(ImportParams{ProjectName: "p", Hostname: "zbox", Image: "https://example.com/b/base.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project:\n  name: p\n", "type: zbox@1\n", "base: zbox@1\n", "- port: 8080\n", "- port: 8081\n", "minContainers: 1\n    maxContainers: 1\n    verticalAutoscaling:\n      cpuMode: DEDICATED\n      minCpu: 2\n      maxCpu: 2\n      minRam: 8\n      maxRam: 8\n      minDisk: 50\n      maxDisk: 50\n", "initCommands:\n              - /opt/zerops/bin/zerops-zbox-orchestrator migrate\n              - /opt/zerops/bin/zerops-zbox-orchestrator host prepare\n              - /opt/zerops/bin/zerops-zbox-orchestrator base ensure https://example.com/b/base.tar.gz\n            start: /opt/zerops/bin/zerops-zbox-orchestrator run\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, stale := range []string{"docker", "mkdir", "sudo", "envSecrets", "apiKey", "envVariables", "ZBOX_SUBSTRATE_KIND"} {
		if strings.Contains(out, stale) {
			t.Errorf("stale %q in:\n%s", stale, out)
		}
	}
	out, err = RenderImport(ImportParams{Hostname: "zbox", Image: "ubuntu:26.04", Templates: []ImportTemplate{{Name: "dev", MemoryBytes: 4 << 30, Cpus: 2}, {Name: "small"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "base ensure ubuntu:26.04\n              - /opt/zerops/bin/zerops-zbox-orchestrator template ensure --name dev --base ubuntu:26.04 --memory 4294967296 --cpus 2\n              - /opt/zerops/bin/zerops-zbox-orchestrator template ensure --name small --base ubuntu:26.04\n            start:"; !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	if strings.Contains(out, "project:") || !strings.HasPrefix(out, "services:") {
		t.Errorf("service-stack import must start at services:\n%s", out)
	}
}

// Image and template names land unquoted in a command line, anything a shell or YAML would read into is refused.
func TestRenderImportRefuses(t *testing.T) {
	for _, p := range []ImportParams{
		{Hostname: "zbox"},
		{Hostname: "zbox", Image: "alpine; rm -rf /"},
		{Hostname: "zbox", Image: "--credential-env"},
		{Hostname: "zbox", Image: "alpine #x"},
		{Hostname: "zbox", Image: "alpine", Templates: []ImportTemplate{{Name: "a b"}}},
		{Hostname: "zbox", Image: "alpine", Templates: []ImportTemplate{{Name: "$(id)"}}},
	} {
		if out, err := RenderImport(p); err == nil {
			t.Errorf("%+v rendered:\n%s", p, out)
		}
	}
}
