// Package globmatch provides a single glob-matching helper shared by the
// archiver (exclude patterns) and restorer (include patterns).
//
// It uses github.com/bmatcuk/doublestar so that `**` matches across directory
// boundaries (e.g. `**/node_modules/**`, `**/report.xlsx`), which the standard
// library's path/filepath.Match does not support. The historical bakku
// behaviour is preserved:
//
//   - Paths are normalized to slash separators before matching, so patterns
//     written with `/` work identically on Windows.
//   - A pattern is matched against both the base name (when the pattern itself
//     contains no `/`, e.g. `*.txt`) and the full relative path, so simple
//     base-name globs keep matching files at any depth.
//   - Invalid patterns never abort the caller; Match reports a non-nil error
//     for the offending pattern and treats it as "no match" so callers may warn
//     (or silently ignore) without failing the whole backup/restore.
package globmatch

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Match reports whether the slash-or-OS-separated relative path matches the
// glob pattern, honouring `**`. It mirrors the previous filepath.Match-based
// logic: the pattern is tried against the file's base name (only when the
// pattern has no `/`) and against the full relative path.
//
// The returned error is non-nil only when the pattern is syntactically invalid
// (doublestar.ErrBadPattern); in that case ok is false. Callers may log a
// warning and continue.
func Match(pattern, relPath string) (ok bool, err error) {
	p := filepath.ToSlash(pattern)
	rel := filepath.ToSlash(relPath)

	// Base-name match, only for patterns without a path separator (e.g.
	// `*.txt`, `report.xlsx`). This preserves the historical behaviour where a
	// bare glob matched a file by name at any depth.
	if !strings.Contains(p, "/") {
		matched, merr := doublestar.Match(p, path_base(rel))
		if merr != nil {
			return false, merr
		}
		if matched {
			return true, nil
		}
	}

	// Full relative-path match (handles `docs/*`, `src/docs/doc1.txt`,
	// `**/node_modules/**`, `**/report.xlsx`, ...).
	matched, merr := doublestar.Match(p, rel)
	if merr != nil {
		return false, merr
	}
	return matched, nil
}

// MatchAny reports whether relPath matches any of the patterns. A syntactically
// invalid pattern is skipped (it never matches and never aborts); it is
// reported via badPattern so callers can warn once. When patterns is empty,
// MatchAny returns false.
func MatchAny(patterns []string, relPath string) (ok bool, badPattern string) {
	for _, pat := range patterns {
		matched, err := Match(pat, relPath)
		if err != nil {
			if badPattern == "" {
				badPattern = pat
			}
			continue
		}
		if matched {
			return true, ""
		}
	}
	return false, badPattern
}

// path_base returns the final slash-separated element of p (like path.Base but
// without importing path for a single use, and operating purely on the
// already-slash-normalized string).
func path_base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
