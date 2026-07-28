// Package yamlutil wraps github.com/goccy/go-yaml with this codebase's
// fixed encoding options — Indent(4) + IndentSequence(true) — so every
// caller produces the same on-disk shape without repeating those options
// (or forgetting them) at each of the many Marshal call sites across the
// codebase. That shape is deliberately byte-compatible with what
// gopkg.in/yaml.v3's default encoder produced before the migration to
// goccy (four-space indent, sequences indented under their parent key),
// since existing conventions elsewhere — gitstore/repair.go's
// "    - "-prefixed block-sequence-item boundary detection,
// task.indentBlock's own four-space prefix — depend on it exactly.
//
// Every Marshal in this codebase should go through here, not call goccy
// directly, so the format can never silently drift between call sites.
package yamlutil

import (
	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// Marshal encodes v with this codebase's fixed formatting options.
func Marshal(v interface{}) ([]byte, error) {
	return yaml.MarshalWithOptions(v, yaml.Indent(4), yaml.IndentSequence(true))
}

// Unmarshal decodes data into v. No options needed — goccy's decoder
// reads a fixed-indent document (or any other validly-indented one) the
// same way regardless of how it was written.
func Unmarshal(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

// Quoted wraps a string so it always encodes as a double-quoted YAML
// scalar when marshaled via Marshal, regardless of goccy's own
// plain/single-quoted style heuristics — verified empirically that those
// heuristics don't safely handle every character: a literal tab byte in
// an otherwise-plain string silently corrupts on round-trip (decodes back
// with the tab and surrounding structure gone) when left to goccy's
// default style choice, and UseSingleQuote doesn't fix it either.
// Double-quoted style has no such ambiguity — every byte is either
// printed literally or explicitly escaped.
//
// Use this as a field's type inside the anonymous struct a type's own
// MarshalYAML() (interface{}, error) method returns, for any field that
// may hold raw external content (tool output, LLM prose) rather than a
// short controlled-vocabulary value — see task.ConversationMessage,
// task.ConversationToolCall, task.ConversationToolActivity, and
// task.ExecutionLogEvent for the pattern. omitempty on a Quoted field
// works the same as on a plain string (its zero value is "").
type Quoted string

// MarshalYAML implements goccy's BytesMarshaler interface.
func (s Quoted) MarshalYAML() ([]byte, error) {
	n := ast.String(token.DoubleQuote(string(s), string(s), &token.Position{}))
	return []byte(n.String()), nil
}
