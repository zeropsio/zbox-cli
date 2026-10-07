package zerops

import (
	"strings"
	"testing"
)

// The YAML of the managed type carries the size pinned as min and max, subdomain access and the first base in ZBOX_WORKSPACE_BASES, and nothing of the runtime days: no containers count, no setup, no zerops.yml, no commands, no key.
func TestRenderImport(t *testing.T) {
	out, err := RenderImport(ImportParams{ProjectName: "p", Hostname: "zbox", Cpu: 24, Ram: 128, Disk: 250, Image: "https://example.com/b/base.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project:\n  name: p\n", "type: zbox@1\n", "enableSubdomainAccess: true\n", "verticalAutoscaling:\n      cpuMode: DEDICATED\n      minCpu: 24\n      maxCpu: 24\n      minRam: 128\n      maxRam: 128\n      minDisk: 250\n      maxDisk: 250\n", "envVariables:\n      ZBOX_WORKSPACE_BASES: https://example.com/b/base.tar.gz\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, stale := range []string{"docker", "mkdir", "sudo", "envSecrets", "apiKey", "ZBOX_SUBSTRATE_KIND", "minContainers", "maxContainers", "zeropsSetup", "zeropsYaml", "initCommands", "start:", "ports:", "template ensure", "base ensure"} {
		if strings.Contains(out, stale) {
			t.Errorf("stale %q in:\n%s", stale, out)
		}
	}
	out, err = RenderImport(ImportParams{Hostname: "zbox", Cpu: 2, Ram: 8, Disk: 50, Image: "ubuntu:26.04"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "ZBOX_WORKSPACE_BASES: ubuntu:26.04\n"; !strings.Contains(out, want) {
		t.Errorf("missing %q in:\n%s", want, out)
	}
	if strings.Contains(out, "project:") || !strings.HasPrefix(out, "services:") {
		t.Errorf("service-stack import must start at services:\n%s", out)
	}
}

// The image lands as a plain scalar the orchestrator splits on whitespace and commas, anything YAML or that split would read into is refused, and so is a size under the driver floor.
func TestRenderImportRefuses(t *testing.T) {
	sized := func(image string) ImportParams {
		return ImportParams{Hostname: "zbox", Cpu: 2, Ram: 2, Disk: 10, Image: image}
	}
	for _, p := range []ImportParams{
		sized(""),
		sized("alpine; rm -rf /"),
		sized("--credential-env"),
		sized("alpine #x"),
		sized("alpine,ubuntu"),
		sized("alpine ubuntu"),
		sized("alpine\nubuntu"),
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
