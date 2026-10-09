package zerops

import (
	"strings"
	"testing"
)

// The YAML of the managed type carries subdomain access and the size pinned as min and max for every resource given, and nothing else: no base
// (the setup registers them through the API), no containers count, no setup, no zerops.yml, no commands, no key.
func TestRenderImport(t *testing.T) {
	out, err := RenderImport(ImportParams{ProjectName: "p", Hostname: "zbox", Cpu: 24, Ram: 128, Disk: 250})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project:\n  name: p\n", "type: zbox@1\n", "enableSubdomainAccess: true\n", "verticalAutoscaling:\n      cpuMode: DEDICATED\n      minCpu: 24\n      maxCpu: 24\n      minRam: 128\n      maxRam: 128\n      minDisk: 250\n      maxDisk: 250\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, stale := range []string{"docker", "mkdir", "sudo", "envSecrets", "envVariables", "ZBOX_WORKSPACE_BASES", "apiKey", "ZBOX_SUBSTRATE_KIND", "minContainers", "maxContainers", "zeropsSetup", "zeropsYaml", "initCommands", "start:", "ports:", "template ensure", "base ensure"} {
		if strings.Contains(out, stale) {
			t.Errorf("stale %q in:\n%s", stale, out)
		}
	}
	out, err = RenderImport(ImportParams{Hostname: "zbox", Ram: 8})
	if err != nil {
		t.Fatal(err)
	}
	if want := "verticalAutoscaling:\n      minRam: 8\n      maxRam: 8\n"; !strings.Contains(out, want) || strings.Contains(out, "Cpu") || strings.Contains(out, "Disk") {
		t.Errorf("only the ram should be pinned in:\n%s", out)
	}
	if strings.Contains(out, "project:") || !strings.HasPrefix(out, "services:") {
		t.Errorf("service-stack import must start at services:\n%s", out)
	}
	out, err = RenderImport(ImportParams{Hostname: "zbox"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "verticalAutoscaling") {
		t.Errorf("no size given, the platform's default should apply:\n%s", out)
	}
}

// A size under the driver floor is refused, a resource left at zero is the platform's.
func TestRenderImportRefuses(t *testing.T) {
	for _, p := range []ImportParams{
		{Hostname: "zbox", Cpu: 1, Ram: 8, Disk: 50},
		{Hostname: "zbox", Cpu: 2, Ram: 1, Disk: 50},
		{Hostname: "zbox", Cpu: 2, Ram: 8, Disk: 9},
		{Hostname: "zbox", Disk: -1},
	} {
		if out, err := RenderImport(p); err == nil {
			t.Errorf("%+v rendered:\n%s", p, out)
		}
	}
}
