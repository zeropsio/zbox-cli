package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
	gitignore "github.com/sabhiram/go-gitignore"
)

// packReport is what the packer did with a directory, printed as one line and, with --verbose, member by member.
type packReport struct {
	// source is the file the exclusions came from, `.dockerignore` or `.gitignore`, empty when nothing was consulted.
	source string
	files  int
	bytes  int64
	packed []string
	// ignored are the entries left out, a pruned directory as one entry with a trailing slash.
	ignored []string
}

func (r packReport) summary() string {
	s := fmt.Sprintf("packed %d files, %s", r.files, bytesString(r.bytes))
	if r.source != "" {
		s += fmt.Sprintf(", %d left out by %s", len(r.ignored), r.source)
	}
	return s
}

// print writes the members and then the summary, the packed ones with +, the ignored with -.
func (r packReport) print(w io.Writer, verbose bool) {
	if verbose {
		for _, m := range r.packed {
			fmt.Fprintln(w, "+", m)
		}
		for _, m := range r.ignored {
			fmt.Fprintln(w, "-", m)
		}
	}
	fmt.Fprintln(w, r.summary())
}

// buildContext answers the archive `zbox base build` sends with its size: a ready archive is sent as it is, a directory is packed into a
// tar.gz in a temporary file (removed on close), with the files' modes and symlinks kept and the ignored entries left out.
func buildContext(source string) (io.ReadCloser, int64, packReport, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, 0, packReport{}, err
	}
	if !info.IsDir() {
		f, err := os.Open(source)
		if err != nil {
			return nil, 0, packReport{}, err
		}
		return f, info.Size(), packReport{files: 1, bytes: info.Size(), packed: []string{filepath.Base(source)}}, nil
	}
	if _, err := os.Stat(filepath.Join(source, "Dockerfile")); err != nil {
		return nil, 0, packReport{}, fmt.Errorf("no Dockerfile in %s", source)
	}
	temp, err := os.CreateTemp("", "zbox-context-*.tar.gz")
	if err != nil {
		return nil, 0, packReport{}, err
	}
	report, err := packDir(source, temp)
	if err == nil {
		var size int64
		if size, err = temp.Seek(0, io.SeekCurrent); err == nil {
			_, err = temp.Seek(0, io.SeekStart)
			return tempFile{temp}, size, report, err
		}
	}
	_ = temp.Close()
	_ = os.Remove(temp.Name())
	return nil, 0, packReport{}, err
}

type tempFile struct{ *os.File }

func (f tempFile) Close() error {
	err := f.File.Close()
	_ = os.Remove(f.Name())
	return err
}

// packDir writes the directory as a tar.gz. The exclusions are the ones `docker build` applies: a `.dockerignore` at the root when there is one,
// the `.gitignore` files of the tree otherwise, and `.git` always. The Dockerfile and the `.dockerignore` go in whatever the patterns say,
// so BuildKit applies the file on the workspace as well.
func packDir(dir string, out io.Writer) (packReport, error) {
	ignore, err := newIgnorer(dir)
	if err != nil {
		return packReport{}, err
	}
	report := packReport{source: ignore.source}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() && info.Name() == ".git" {
			report.ignored = append(report.ignored, rel+"/")
			return filepath.SkipDir
		}
		skip, prune, err := ignore.excludes(p, rel, info.IsDir())
		if err != nil {
			return err
		}
		if skip && prune {
			report.ignored = append(report.ignored, rel+"/")
			return filepath.SkipDir
		}
		if skip {
			if !info.IsDir() {
				report.ignored = append(report.ignored, rel)
			}
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = rel
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		report.packed = append(report.packed, rel)
		if !info.Mode().IsRegular() {
			return nil
		}
		report.files++
		report.bytes += info.Size()
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		_ = f.Close()
		return err
	})
	if err != nil {
		return packReport{}, err
	}
	if err := tw.Close(); err != nil {
		return packReport{}, err
	}
	return report, gz.Close()
}

// ignorer decides what stays out of the context: Docker's matcher over the root `.dockerignore`, or the tree's `.gitignore` files.
type ignorer struct {
	source string
	docker *patternmatcher.PatternMatcher
	// git holds the rules of every .gitignore met so far by the directory it governs, the root's under "".
	git map[string][]gitRule
}

// gitRule is one line of a .gitignore, compiled on its own so the last matching line of the deepest file decides, `!` lines included.
type gitRule struct {
	negate bool
	match  *gitignore.GitIgnore
}

func newIgnorer(dir string) (*ignorer, error) {
	f, err := os.Open(filepath.Join(dir, ".dockerignore"))
	if err == nil {
		defer f.Close()
		patterns, err := ignorefile.ReadAll(f)
		if err != nil {
			return nil, fmt.Errorf(".dockerignore: %w", err)
		}
		matcher, err := patternmatcher.New(patterns)
		if err != nil {
			return nil, fmt.Errorf(".dockerignore: %w", err)
		}
		return &ignorer{source: ".dockerignore", docker: matcher}, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	i := &ignorer{git: map[string][]gitRule{}}
	if err := i.learnGitignore(dir, ""); err != nil {
		return nil, err
	}
	return i, nil
}

// learnGitignore keeps the directory's .gitignore under its path, the root's under "".
func (i *ignorer) learnGitignore(p string, rel string) error {
	raw, err := os.ReadFile(filepath.Join(p, ".gitignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var rules []gitRule
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, " \r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := strings.HasPrefix(line, "!")
		rules = append(rules, gitRule{negate: negate, match: gitignore.CompileIgnoreLines(strings.TrimPrefix(line, "!"))})
	}
	i.git[rel] = rules
	i.source = ".gitignore"
	return nil
}

// excludes answers whether the entry stays out and whether a directory can be pruned whole: under .dockerignore only when no `!` pattern
// could bring a child back, under .gitignore always, since git never looks into an ignored directory.
func (i *ignorer) excludes(p string, rel string, isDir bool) (bool, bool, error) {
	if rel == "Dockerfile" || rel == ".dockerignore" {
		return false, false, nil
	}
	if i.docker != nil {
		matched, err := i.docker.MatchesOrParentMatches(rel)
		if err != nil {
			return false, false, err
		}
		return matched, matched && isDir && !i.docker.Exclusions(), nil
	}
	// The walk visits a directory before its children, so its .gitignore is known by the time they are asked about.
	if isDir {
		if err := i.learnGitignore(p, rel); err != nil {
			return false, false, err
		}
	}
	// From the entry's directory up to the root: the last matching line of the deepest .gitignore decides.
	for dir := path.Dir(rel); ; dir = path.Dir(dir) {
		key := dir
		if key == "." {
			key = ""
		}
		sub := rel
		if key != "" {
			sub = strings.TrimPrefix(rel, key+"/")
		}
		rules := i.git[key]
		for n := len(rules) - 1; n >= 0; n-- {
			if rules[n].match.MatchesPath(sub) || (isDir && rules[n].match.MatchesPath(sub+"/")) {
				excluded := !rules[n].negate
				return excluded, excluded && isDir, nil
			}
		}
		if key == "" {
			return false, false, nil
		}
	}
}
