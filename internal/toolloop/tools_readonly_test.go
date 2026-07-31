package toolloop

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGrepToolFindsMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello world\nfoo bar\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt:1:hello world") {
		t.Fatalf("expected match in path:line:text form, got %q", out)
	}
}

func TestGrepToolSupportsRegex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "value123\nvalueABC\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"value[0-9]+"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "value123") {
		t.Fatalf("expected regex match, got %q", out)
	}
	if strings.Contains(out, "valueABC") {
		t.Fatalf("regex should not match non-numeric suffix: %q", out)
	}
}

func TestGrepToolContextLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "line1\nline2\nMATCHME\nline4\nline5\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"MATCHME","context":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line2") || !strings.Contains(out, "line4") {
		t.Fatalf("expected one line of context on each side, got %q", out)
	}
	// Context lines use rg's `-` separator (not `:`) between path/line/content.
	if !strings.Contains(out, "a.txt-2-line2") {
		t.Fatalf("expected context-line separator shape a.txt-2-line2, got %q", out)
	}
	if !strings.Contains(out, "a.txt:3:MATCHME") {
		t.Fatalf("expected match-line separator shape a.txt:3:MATCHME, got %q", out)
	}
}

func TestGrepToolIgnoreCase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "Hello World\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"hello world","ignore_case":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Hello World") {
		t.Fatalf("expected case-insensitive match, got %q", out)
	}
}

func TestGrepToolCaseSensitiveByDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "Hello World\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"hello world"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "no matches" {
		t.Fatalf("expected no matches without ignore_case, got %q", out)
	}
}

func TestGrepToolGlobFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "needle\n")
	writeFile(t, dir, "b.md", "needle\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"needle","glob":"*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("expected a.go in results, got %q", out)
	}
	if strings.Contains(out, "b.md") {
		t.Fatalf("glob should have excluded b.md, got %q", out)
	}
}

func TestGrepToolNoMatches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello world\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"nonexistentpattern"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "no matches" {
		t.Fatalf("expected the no-matches string, got %q", out)
	}
}

func TestGrepToolInvalidRegexSurfacesStderr(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "hello world\n")
	g := grepTool{}

	_, err := g.Execute(context.Background(), dir, `{"pattern":"("}`)
	if err == nil {
		t.Fatal("expected an error for invalid regex")
	}
	if !strings.Contains(err.Error(), "regex") {
		t.Fatalf("expected rg's stderr (regex parse error) surfaced, got %v", err)
	}
}

func TestGrepToolMissingRGProducesActionableError(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { lookPath = original }()

	dir := t.TempDir()
	g := grepTool{}

	_, err := g.Execute(context.Background(), dir, `{"pattern":"hello"}`)
	if err == nil {
		t.Fatal("expected an error when rg is not on PATH")
	}
	if !strings.Contains(err.Error(), "ripgrep") {
		t.Fatalf("expected an actionable ripgrep install message, got %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(err.Error(), "winget") {
			t.Fatalf("expected a winget install hint on Windows, got %v", err)
		}
	}
}

func TestGrepToolScopesToSubdirPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/a.txt", "needle\n")
	writeFile(t, dir, "other/b.txt", "needle\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"needle","path":"sub"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("expected match within scoped subdir, got %q", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Fatalf("path scoping should have excluded other/b.txt, got %q", out)
	}
}

func TestGrepToolPathNormalizedToForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/a.txt", "needle\n")
	g := grepTool{}

	out, err := g.Execute(context.Background(), dir, `{"pattern":"needle"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sub/a.txt") {
		t.Fatalf("expected forward-slash-normalized path, got %q", out)
	}
	if strings.Contains(out, `sub\a.txt`) {
		t.Fatalf("path should not contain a backslash separator, got %q", out)
	}
}

func TestGrepToolRequiresPattern(t *testing.T) {
	dir := t.TempDir()
	g := grepTool{}
	if _, err := g.Execute(context.Background(), dir, `{}`); err == nil {
		t.Fatal("expected pattern-required error")
	}
}

func TestGrepToolRejectsInvalidArguments(t *testing.T) {
	dir := t.TempDir()
	g := grepTool{}
	if _, err := g.Execute(context.Background(), dir, `not json`); err == nil {
		t.Fatal("expected invalid-arguments error")
	}
}
