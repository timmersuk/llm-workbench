package gitstore

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// repairTornYAMLFiles walks each of paths (always absolute — a mix of
// directories and/or individual file paths, see addAndCommit's doc
// comment for the contract) and validates every .yaml file found under
// them in place, before they're staged for commit. A file that fails to
// parse is the signature of a write interrupted mid-flight —
// task.FileStore's append-only writers (AppendExecutionLogEvent,
// AppendConversationMessages) guarantee bytes written by an earlier call
// are never touched by a later one, so a torn file can only ever have a
// broken *trailing* entry, never broken content earlier in the file.
// repairTornYAMLFile trims back to the last complete top-level list entry
// and rewrites the file; a file that can't be repaired that way (e.g. it
// isn't a trailing-list-entry document at all, like task.yaml) is left on
// disk untouched, and its (absolute) path is returned so the caller can
// exclude it from this commit rather than bake broken content permanently
// into git history.
//
// Rare by design (see this package's commit.go/push.go doc comments):
// every legitimate write already lands fully before its pending change is
// even enqueued, so this only ever fires on the genuine unlucky-timing
// case of a crash mid-write, not as a normal code path.
func repairTornYAMLFiles(paths []string) (unrepaired []string) {
	seen := map[string]bool{}
	visit := func(path string) {
		if filepath.Ext(path) != ".yaml" || seen[path] {
			return
		}
		seen[path] = true
		if !repairTornYAMLFile(path) {
			unrepaired = append(unrepaired, path)
		}
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if !os.IsNotExist(err) {
				logrus.WithError(err).WithField("path", p).Warn("unexpected error checking a pending path before yaml validation; skipping it this tick")
			}
			// Deleted or otherwise inaccessible (os.IsNotExist): this
			// package has no delete-type operation today (commit.go is
			// entirely Create/Update/Append/Record), so that case is never
			// expected to be hit in practice — skip rather than guess at
			// handling. Any *other* error got a log line above instead of
			// being swallowed identically to the expected case.
			continue
		}
		if !info.IsDir() {
			visit(p)
			continue
		}
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			visit(path)
			return nil
		})
	}
	return unrepaired
}

// repairTornYAMLFile validates path in place, repairing it if needed.
// Returns true if the file is now valid YAML (whether it always was, or
// was just repaired), false if it's broken in a way trimming trailing
// list entries can't fix.
func repairTornYAMLFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	var probe interface{}
	if yaml.Unmarshal(data, &probe) == nil {
		return true
	}

	repaired, droppedLines, ok := repairTornYAML(data)
	if !ok {
		logrus.WithField("path", path).Error("yaml file failed to parse and could not be automatically repaired (not a trailing-list-entry tear); excluding it from this commit")
		return false
	}

	if err := os.WriteFile(path, repaired, 0o644); err != nil {
		logrus.WithError(err).WithField("path", path).Error("failed to write repaired yaml file")
		return false
	}
	logrus.WithFields(logrus.Fields{"path": path, "dropped_lines": droppedLines}).
		Warn("yaml file had a torn trailing entry (most likely an interrupted write from a crash mid-run) -- trimmed back to its last complete entry before committing")
	return true
}

// repairTornYAML trims data back to its last complete top-level list entry
// and reports whether the result parses as valid YAML. Candidate cut
// points are lines that open a new top-level list entry at yaml.v3's own
// default block-sequence indent ("    - ", four spaces then a dash) —
// task.FileStore's append-only writers only ever add whole entries at
// that exact indent, so a torn write can only ever leave a partial entry
// starting at one of these boundaries, never break content earlier in the
// file. Tries the most recent boundary first (the smallest possible
// loss); only falls back to an earlier one if the immediate trim still
// doesn't parse, which the append-only write guarantee above means should
// never actually happen in practice — kept as defense in depth, not a
// case this is expected to exercise.
func repairTornYAML(data []byte) (repaired []byte, droppedLines int, ok bool) {
	lines := strings.Split(string(data), "\n")

	var cuts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "    - ") {
			cuts = append(cuts, i)
		}
	}

	for i := len(cuts) - 1; i >= 0; i-- {
		candidate := strings.Join(lines[:cuts[i]], "\n") + "\n"
		var probe interface{}
		if yaml.Unmarshal([]byte(candidate), &probe) == nil {
			return []byte(candidate), len(lines) - cuts[i], true
		}
	}

	return nil, 0, false
}
