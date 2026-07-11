package toolloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteToolCreatesFileAndParentDirs(t *testing.T) {
	dir := t.TempDir()
	w := writeTool{}

	out, err := w.Execute(context.Background(), dir, `{"path":"sub/new.txt","content":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wrote") {
		t.Fatalf("unexpected confirmation: %q", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Fatalf("wrong content: %q", string(b))
	}
}

func TestWriteToolOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o644)
	w := writeTool{}

	if _, err := w.Execute(context.Background(), dir, `{"path":"a.txt","content":"new"}`); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "new" {
		t.Fatalf("expected overwrite, got %q", string(b))
	}
}

func TestWriteToolRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	w := writeTool{}
	if _, err := w.Execute(context.Background(), dir, `{"path":"../escape.txt","content":"x"}`); err == nil {
		t.Fatal("expected path-escape rejection")
	}
}

func TestWriteToolRequiresPath(t *testing.T) {
	dir := t.TempDir()
	w := writeTool{}
	if _, err := w.Execute(context.Background(), dir, `{"content":"x"}`); err == nil {
		t.Fatal("expected path-required error")
	}
}

func TestEditToolReplacesUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("func Foo() {}\nfunc Bar() {}"), 0o644)
	e := editTool{}

	out, err := e.Execute(context.Background(), dir, `{"path":"a.txt","old_string":"func Foo() {}","new_string":"func Foo() { return }"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replaced 1") {
		t.Fatalf("unexpected confirmation: %q", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "func Foo() { return }\nfunc Bar() {}" {
		t.Fatalf("unexpected content: %q", string(b))
	}
}

func TestEditToolRejectsAmbiguousMatchWithoutReplaceAll(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\nx\nx"), 0o644)
	e := editTool{}

	if _, err := e.Execute(context.Background(), dir, `{"path":"a.txt","old_string":"x","new_string":"y"}`); err == nil {
		t.Fatal("expected ambiguous-match rejection")
	}
	// File must be untouched by the rejected edit.
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "x\nx\nx" {
		t.Fatalf("file should be unchanged after a rejected edit, got %q", string(b))
	}
}

func TestEditToolReplaceAllReplacesEveryOccurrence(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\nx\nx"), 0o644)
	e := editTool{}

	out, err := e.Execute(context.Background(), dir, `{"path":"a.txt","old_string":"x","new_string":"y","replace_all":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replaced 3") {
		t.Fatalf("unexpected confirmation: %q", out)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "y\ny\ny" {
		t.Fatalf("unexpected content: %q", string(b))
	}
}

func TestEditToolRejectsMissingOldString(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644)
	e := editTool{}

	if _, err := e.Execute(context.Background(), dir, `{"path":"a.txt","old_string":"nonexistent","new_string":"y"}`); err == nil {
		t.Fatal("expected not-found rejection")
	}
}

func TestEditToolRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	e := editTool{}
	if _, err := e.Execute(context.Background(), dir, `{"path":"../escape.txt","old_string":"x","new_string":"y"}`); err == nil {
		t.Fatal("expected path-escape rejection")
	}
}

func TestExecutionToolsIncludesReadOnlyAndWriteTools(t *testing.T) {
	names := make(map[string]bool)
	for _, tl := range ExecutionTools() {
		names[toolName(tl)] = true
	}
	for _, want := range []string{"read_file", "grep_search", "glob", "write_file", "edit_file"} {
		if !names[want] {
			t.Fatalf("ExecutionTools missing %s: %v", want, names)
		}
	}
}

func TestWriteAndEditToolSpecsValid(t *testing.T) {
	for _, tl := range []Tool{writeTool{}, editTool{}} {
		var schema map[string]any
		if err := json.Unmarshal(tl.Spec().Function.Parameters, &schema); err != nil {
			t.Fatalf("%s has invalid parameter JSON: %v", toolName(tl), err)
		}
	}
}
