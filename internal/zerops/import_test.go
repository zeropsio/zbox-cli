package zerops

import (
	"strings"
	"testing"
)

// The YAML has to publish both hosts, carry no key of its own (the controller generates ZBOX_WORKSPACE_API_KEY), set no substrate env (Firecracker is the only one) and run migrate, host prepare, base ensure and the template lines in that order before the mounted orchestrator starts, without sudo.
func TestRenderImport(t *testing.T) {
	out, err := RenderImport(ImportParams{ProjectName: "p", Hostname: "zbox", Cpu: 24, Ram: 128, Disk: 250, Image: "https://example.com/b/base.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project:\n  name: p\n", "type: zbox@1\n", "base: zbox@1\n", "- port: 8080\n", "- port: 8081\n", "minContainers: 1\n    maxContainers: 1\n    verticalAutoscaling:\n      cpuMode: DEDICATED\n      minCpu: 24\n      maxCpu: 24\n      minRam: 128\n      maxRam: 128\n      minDisk: 250\n      maxDisk: 250\n", "initCommands:\n              - /opt/zerops/bin/zerops-zbox-orchestrator migrate\n              - /opt/zerops/bin/zerops-zbox-orchestrator host prepare\n              - /opt/zerops/bin/zerops-zbox-orchestrator base ensure https://example.com/b/base.tar.gz\n            start: /opt/zerops/bin/zerops-zbox-orchestrator run\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, stale := range []string{"docker", "mkdir", "sudo", "envSecrets", "apiKey", "envVariables", "ZBOX_SUBSTRATE_KIND"} {
		if strings.Contains(out, stale) {
			t.Errorf("stale %q in:\n%s", stale, out)
		}
	}
	out, err = RenderImport(ImportParams{Hostname: "zbox", Cpu: 2, Ram: 8, Disk: 50, Image: "ubuntu:26.04", Templates: []ImportTemplate{{Name: "dev", MemoryBytes: 4 << 30, Cpus: 2}, {Name: "small"}}})
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

// Image and template names land unquoted in a command line, anything a shell or YAML would read into is refused, and so is a size under the driver floor.
func TestRenderImportRefuses(t *testing.T) {
	small := ImportParams{Hostname: "zbox", Cpu: 2, Ram: 2, Disk: 10}
	sized := func(image string, templates ...ImportTemplate) ImportParams {
		p := small
		p.Image, p.Templates = image, templates
		return p
	}
	for _, p := range []ImportParams{
		small,
		sized("alpine; rm -rf /"),
		sized("--credential-env"),
		sized("alpine #x"),
		sized("alpine", ImportTemplate{Name: "a b"}),
		sized("alpine", ImportTemplate{Name: "$(id)"}),
		{Hostname: "zbox", Cpu: 1, Ram: 8, Disk: 50, Image: "alpine"},
		{Hostname: "zbox", Cpu: 2, Ram: 1, Disk: 50, Image: "alpine"},
		{Hostname: "zbox", Cpu: 2, Ram: 8, Disk: 9, Image: "alpine"},
		{Hostname: "zbox", Image: "alpine"},
	} {
		if out, err := RenderImport(p); err == nil {
			t.Errorf("%+v rendered:\n%s", p, out)
		}
	}
}
