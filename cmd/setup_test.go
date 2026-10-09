package cmd

import "testing"

// The chain flags share the [name=]value shape, and a template says everything: name=cpus:memory@base.
func TestSplitNamed(t *testing.T) {
	for in, want := range map[string][2]string{
		"api=./services/api":       {"api", "./services/api"},
		"py=python:3.13-slim":      {"py", "python:3.13-slim"},
		"ubuntu:26.04":             {"", "ubuntu:26.04"},
		"https://x.y/z.tar.gz?a=b": {"", "https://x.y/z.tar.gz?a=b"},
		"./dir=with=equals/ctx":    {"", "./dir=with=equals/ctx"},
		"my.app=./ctx":             {"", "my.app=./ctx"},
		"=./ctx":                   {"", "=./ctx"},
	} {
		if name, value := splitNamed(in); name != want[0] || value != want[1] {
			t.Errorf("%q: got %q, %q, want %q, %q", in, name, value, want[0], want[1])
		}
	}
}

func TestSetupTemplates(t *testing.T) {
	out, err := setupTemplates([]string{"dev=2:4g@hello", "tiny=1:512m@alp"}, []string{"dev=claude --version"})
	if err != nil || len(out) != 2 {
		t.Fatalf("%+v, %v", out, err)
	}
	if out[0].Name != "dev" || out[0].Cpus != 2 || out[0].MemoryBytes != 4<<30 || out[0].Base != "hello" || out[0].Warmup != "claude --version" {
		t.Errorf("dev: %+v", out[0])
	}
	if out[1].Base != "alp" || out[1].MemoryBytes != 512<<20 || out[1].Warmup != "" {
		t.Errorf("tiny: %+v", out[1])
	}
	for _, bad := range [][]string{
		{"dev"}, {"dev=2:4g"}, {"dev=:4g@hello"}, {"dev=2:@hello"}, {"dev=x:4g@hello"}, {"dev=2:4g@"}, {"=2:4g@hello"}, {"dev=2:4g@hello", "dev=1:1g@hello"},
	} {
		if out, err := setupTemplates(bad, nil); err == nil {
			t.Errorf("%v parsed: %+v", bad, out)
		}
	}
	if _, err := setupTemplates([]string{"dev=2:4g@hello"}, []string{"big=x"}); err == nil {
		t.Error("a warm-up for a template that is not there parsed")
	}
}

func TestCheckTemplateBases(t *testing.T) {
	builds := []*setupBuild{{name: "hello"}}
	pulls := []setupPull{{name: "alp", ref: "alpine:3.20"}, {ref: "ubuntu:26.04"}}
	for _, base := range []string{"hello", "alp", "alpine:3.20", "ubuntu:26.04"} {
		if err := checkTemplateBases([]setupTemplate{{Name: "t", Base: base}}, builds, pulls); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
	if err := checkTemplateBases([]setupTemplate{{Name: "t", Base: "nope"}}, builds, pulls); err == nil {
		t.Error("an unknown base passed")
	}
	if err := checkTemplateBases(nil, builds, []setupPull{{name: "hello", ref: "alpine:3.20"}}); err == nil {
		t.Error("a pull named like a build passed")
	}
}
