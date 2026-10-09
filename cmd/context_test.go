package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func members(t *testing.T, dir string) ([]string, packReport) {
	t.Helper()
	var buf bytes.Buffer
	report, err := packDir(dir, &buf)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	slices.Sort(names)
	return names, report
}

// A .dockerignore is the rule, with Docker's semantics: a `!` exception brings a file back, the Dockerfile and the file itself always go.
func TestPackDirDockerignore(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"Dockerfile":          "FROM scratch\n",
		".dockerignore":       "node_modules\n*.log\n!keep.log\n.env\nDockerfile\n.gitignore\n",
		".gitignore":          "src\n",
		".env":                "SECRET=1\n",
		"build.log":           "x",
		"keep.log":            "x",
		"src/main.go":         "package main\n",
		"node_modules/a/b.js": "x",
	})
	names, report := members(t, dir)
	want := []string{".dockerignore", "Dockerfile", "keep.log", "src/", "src/main.go"}
	if !slices.Equal(names, want) {
		t.Errorf("members %v, want %v", names, want)
	}
	if report.source != ".dockerignore" || report.files != 4 {
		t.Errorf("report %+v", report)
	}
	for _, left := range []string{".env", ".gitignore", "build.log", "node_modules/a/b.js"} {
		if !slices.Contains(report.ignored, left) {
			t.Errorf("%s not reported as ignored: %v", left, report.ignored)
		}
	}
}

// Without a .dockerignore the tree's .gitignore files apply, nested ones for their directory, an ignored directory is pruned whole, .git always is.
func TestPackDirGitignore(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"Dockerfile":          "FROM scratch\n",
		".gitignore":          "node_modules/\n.env\n*.tmp\n",
		".env":                "SECRET=1\n",
		"a.tmp":               "x",
		"node_modules/a/b.js": "x",
		"src/.gitignore":      "generated/\n!important.tmp\n",
		"src/main.go":         "package main\n",
		"src/important.tmp":   "x",
		"src/other.tmp":       "x",
		"src/generated/x.go":  "x",
		".git/HEAD":           "ref\n",
	})
	names, report := members(t, dir)
	want := []string{".gitignore", "Dockerfile", "src/", "src/.gitignore", "src/important.tmp", "src/main.go"}
	if !slices.Equal(names, want) {
		t.Errorf("members %v, want %v", names, want)
	}
	if report.source != ".gitignore" {
		t.Errorf("report %+v", report)
	}
	for _, left := range []string{".git/", "node_modules/", "src/generated/", ".env", "a.tmp", "src/other.tmp"} {
		if !slices.Contains(report.ignored, left) {
			t.Errorf("%s not reported as ignored: %v", left, report.ignored)
		}
	}
}

// With neither file everything but .git goes, and the report says nothing governed.
func TestPackDirNoIgnore(t *testing.T) {
	dir := writeTree(t, map[string]string{"Dockerfile": "FROM scratch\n", ".env": "x", ".git/HEAD": "x"})
	names, report := members(t, dir)
	if want := []string{".env", "Dockerfile"}; !slices.Equal(names, want) || report.source != "" || len(report.ignored) != 1 {
		t.Errorf("members %v, report %+v", names, report)
	}
}
