package main

import "testing"

func TestRepoDirName(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"https with .git", "https://github.com/timmersuk/llm-workbench-data.git", "llm-workbench-data"},
		{"https without .git", "https://github.com/timmersuk/llm-workbench-data", "llm-workbench-data"},
		{"scp-like ssh", "git@github.com:timmersuk/llm-workbench-data.git", "llm-workbench-data"},
		{"ssh:// URL form", "ssh://git@github.com/timmersuk/llm-workbench-data.git", "llm-workbench-data"},
		{"local windows path, bare-repo .git dir name", `D:\tmp\data-remote.git`, "data-remote"},
		{"local posix path, no .git suffix", "/tmp/repos-root/llm-workbench-data", "llm-workbench-data"},
		{"trailing slash on https URL", "https://github.com/timmersuk/llm-workbench-data.git/", "llm-workbench-data"},
		{"trailing slash on local path", `D:\tmp\llm-workbench-data\`, "llm-workbench-data"},
		{"http (no s)", "http://example.com/org/repo.git", "repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repoDirName(tc.url)
			if err != nil {
				t.Fatalf("repoDirName(%q) returned error: %v", tc.url, err)
			}
			if got != tc.want {
				t.Errorf("repoDirName(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestRepoDirName_ErrorsOnEmpty(t *testing.T) {
	if _, err := repoDirName(""); err == nil {
		t.Error("expected an error for an empty url, got nil")
	}
}
