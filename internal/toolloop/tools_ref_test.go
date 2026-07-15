package toolloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initRefTestRepo creates a real git repository with one commit on main
// (README.md) and a second branch ("feature") that adds an extra file not
// present on main — real `git` subprocess calls, not mocks, since these tools
// are a thin wrapper around the git CLI.
func initRefTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		out, err := runGit(context.Background(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")

	run("checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "add feature")
	run("checkout", "-q", "main")

	return dir
}

func TestReadAtRefTool_ReadsFileContentAtRef(t *testing.T) {
	dir := initRefTestRepo(t)
	r := readAtRefTool{}

	out, err := r.Execute(context.Background(), dir, `{"ref":"feature","path":"feature.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "package main") {
		t.Fatalf("expected feature.go's content, got %q", out)
	}
}

func TestReadAtRefTool_FileAbsentOnCurrentBranchIsAnError(t *testing.T) {
	dir := initRefTestRepo(t)
	r := readAtRefTool{}

	// feature.go only exists on the "feature" branch, not on main (the
	// checked-out HEAD) — confirms this genuinely reads git objects, not the
	// working tree.
	if _, err := r.Execute(context.Background(), dir, `{"ref":"main","path":"feature.go"}`); err == nil {
		t.Fatal("expected an error reading a path that doesn't exist on main")
	}
}

func TestReadAtRefTool_PaginatesLikeReadFile(t *testing.T) {
	dir := initRefTestRepo(t)
	r := readAtRefTool{}

	out, err := r.Execute(context.Background(), dir, `{"ref":"main","path":"README.md","offset":2,"limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line2") || strings.Contains(out, "line1") || strings.Contains(out, "line3") {
		t.Fatalf("expected only line2 from the paginated window, got %q", out)
	}
}

func TestReadAtRefTool_InvalidRefIsAnError(t *testing.T) {
	dir := initRefTestRepo(t)
	r := readAtRefTool{}

	if _, err := r.Execute(context.Background(), dir, `{"ref":"does-not-exist","path":"README.md"}`); err == nil {
		t.Fatal("expected an error for a nonexistent ref")
	}
}

func TestReadAtRefTool_RequiresRefAndPath(t *testing.T) {
	r := readAtRefTool{}
	dir := t.TempDir()

	if _, err := r.Execute(context.Background(), dir, `{"path":"README.md"}`); err == nil {
		t.Fatal("expected ref-required error")
	}
	if _, err := r.Execute(context.Background(), dir, `{"ref":"main"}`); err == nil {
		t.Fatal("expected path-required error")
	}
}

func TestReadAtRefTool_RejectsInvalidArguments(t *testing.T) {
	r := readAtRefTool{}
	if _, err := r.Execute(context.Background(), t.TempDir(), `not json`); err == nil {
		t.Fatal("expected invalid-arguments error")
	}
}

func TestReadAtRefTool_SpecValid(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(readAtRefTool{}.Spec().Function.Parameters, &schema); err != nil {
		t.Fatalf("read_file_at_ref has invalid parameter JSON: %v", err)
	}
}

func TestListFilesAtRefTool_ListsFilesAtRef(t *testing.T) {
	dir := initRefTestRepo(t)
	l := listFilesAtRefTool{}

	out, err := l.Execute(context.Background(), dir, `{"ref":"feature"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "feature.go") || !strings.Contains(out, "README.md") {
		t.Fatalf("expected both files on the feature branch, got %q", out)
	}
}

func TestListFilesAtRefTool_DoesNotListFilesOnlyOnOtherBranches(t *testing.T) {
	dir := initRefTestRepo(t)
	l := listFilesAtRefTool{}

	out, err := l.Execute(context.Background(), dir, `{"ref":"main"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "feature.go") {
		t.Fatalf("main should not list feature.go: %q", out)
	}
	if !strings.Contains(out, "README.md") {
		t.Fatalf("expected README.md on main, got %q", out)
	}
}

func TestListFilesAtRefTool_InvalidRefIsAnError(t *testing.T) {
	dir := initRefTestRepo(t)
	l := listFilesAtRefTool{}

	if _, err := l.Execute(context.Background(), dir, `{"ref":"does-not-exist"}`); err == nil {
		t.Fatal("expected an error for a nonexistent ref")
	}
}

func TestListFilesAtRefTool_RequiresRef(t *testing.T) {
	l := listFilesAtRefTool{}
	if _, err := l.Execute(context.Background(), t.TempDir(), `{}`); err == nil {
		t.Fatal("expected ref-required error")
	}
}

func TestListFilesAtRefTool_RejectsInvalidArguments(t *testing.T) {
	l := listFilesAtRefTool{}
	if _, err := l.Execute(context.Background(), t.TempDir(), `not json`); err == nil {
		t.Fatal("expected invalid-arguments error")
	}
}

func TestListFilesAtRefTool_SpecValid(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(listFilesAtRefTool{}.Spec().Function.Parameters, &schema); err != nil {
		t.Fatalf("list_files_at_ref has invalid parameter JSON: %v", err)
	}
}

func TestReadOnlyToolsIncludesRefAwareTools(t *testing.T) {
	names := make(map[string]bool)
	for _, tl := range ReadOnlyTools() {
		names[toolName(tl)] = true
	}
	if !names["read_file_at_ref"] {
		t.Fatalf("ReadOnlyTools missing read_file_at_ref: %v", names)
	}
	if !names["list_files_at_ref"] {
		t.Fatalf("ReadOnlyTools missing list_files_at_ref: %v", names)
	}
}
