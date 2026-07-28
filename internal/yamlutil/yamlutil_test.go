package yamlutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testMsg struct {
	Role    string `yaml:"role"`
	Content string `yaml:"content"`
}
type testConv struct {
	Stage    string    `yaml:"stage"`
	Messages []testMsg `yaml:"messages"`
}

// TestMarshal_MatchesYAMLv3sHistoricalFourSpaceIndentConvention locks in
// byte-for-byte compatibility with what gopkg.in/yaml.v3's default
// encoder produced before this package existed — every existing
// conversation-*.yaml/execution log on disk, and gitstore/repair.go's
// "    - " boundary detection, depend on this exact shape.
func TestMarshal_MatchesYAMLv3sHistoricalFourSpaceIndentConvention(t *testing.T) {
	conv := testConv{Stage: "review", Messages: []testMsg{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}}

	data, err := Marshal(conv)
	require.NoError(t, err)

	want := "stage: review\nmessages:\n    - role: user\n      content: hi\n    - role: assistant\n      content: hello\n"
	assert.Equal(t, want, string(data))
}

// TestMarshal_RoundTripsContentThatBrokeYAMLv3 locks in the two specific
// yaml.v3 round-trip bugs this migration exists to fix: a leading-newline
// block-scalar (Unmarshal previously failed with "did not find expected
// key"), and leading-space-per-line content shaped like `git diff --stat`
// output (previously corrupted the whole document under yaml.v3's
// block-literal encoder).
func TestMarshal_RoundTripsContentThatBrokeYAMLv3(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"leading newline", "\n\nHere are some questions:\n1. What?\n"},
		{"leading-space lines (git diff --stat shape)", " internal/api/foo.go | 2 +-\n internal/api/bar.go | 4 ++--\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conv := testConv{Stage: "review", Messages: []testMsg{{Role: "assistant", Content: tc.content}}}

			data, err := Marshal(conv)
			require.NoError(t, err)

			var back testConv
			require.NoError(t, Unmarshal(data, &back), "must round-trip cleanly, not fail like yaml.v3 did on this exact shape")
			require.Len(t, back.Messages, 1)
			assert.Equal(t, tc.content, back.Messages[0].Content)
		})
	}
}

// TestUnmarshal_ReadsAnyValidIndentRegardlessOfMarshalOptions confirms
// Unmarshal needs no matching options — every existing yaml.Unmarshal call
// site in the codebase can keep calling Unmarshal unchanged.
func TestUnmarshal_ReadsAnyValidIndentRegardlessOfMarshalOptions(t *testing.T) {
	twoSpaceIndent := "stage: review\nmessages:\n- role: user\n  content: hi\n"
	var conv testConv
	require.NoError(t, Unmarshal([]byte(twoSpaceIndent), &conv))
	require.Len(t, conv.Messages, 1)
	assert.Equal(t, "hi", conv.Messages[0].Content)
}

// TestMarshal_PlainStringSilentlyCorruptsEmbeddedTabs documents the real
// gap that motivates Quoted: goccy's own default plain-scalar style
// heuristic (used for a bare string field with no custom marshaler) does
// not detect that a literal tab byte needs quoting to round-trip safely.
// This is not hypothetical — `go test`'s own summary lines
// ("ok  \tpackage\t0.01s") are exactly this shape, and raw command output
// like that is precisely what execution logs and tool-activity records
// persist. This test intentionally documents the corruption (not asserts
// correctness) so a future goccy upgrade that happens to fix its own
// heuristic doesn't make this look confusing — see
// TestQuoted_RoundTripsTabCharactersThatPlainStyleCorrupts for the actual
// fix this package provides.
func TestMarshal_PlainStringSilentlyCorruptsEmbeddedTabs(t *testing.T) {
	tabby := "ok  \tpackage/foo\t0.01s"
	data, err := Marshal(testMsg{Role: "assistant", Content: tabby})
	require.NoError(t, err)

	var back testMsg
	require.NoError(t, Unmarshal(data, &back))
	assert.NotEqual(t, tabby, back.Content, "documents goccy's default-style gap; if this starts failing, the gap may be fixed upstream and Quoted's workaround could potentially be simplified")
}

type testQuotedMsg struct {
	Role    string `yaml:"role"`
	Content Quoted `yaml:"content"`
}

// TestQuoted_RoundTripsTabCharactersThatPlainStyleCorrupts is the actual
// fix: wrapping the same tab-containing content in Quoted round-trips it
// correctly, unlike the plain string in the test above.
func TestQuoted_RoundTripsTabCharactersThatPlainStyleCorrupts(t *testing.T) {
	tabby := "ok  \tpackage/foo\t0.01s"
	data, err := Marshal(testQuotedMsg{Role: "assistant", Content: Quoted(tabby)})
	require.NoError(t, err)

	var back testQuotedMsg
	require.NoError(t, Unmarshal(data, &back))
	assert.Equal(t, tabby, string(back.Content))
}
