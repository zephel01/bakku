package globmatch

import (
	"path/filepath"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		relPath string
		want    bool
	}{
		// `**` crossing directory boundaries (the primary bug fix).
		{"doublestar node_modules deep", "**/node_modules/**", "src/app/node_modules/pkg/index.js", true},
		{"doublestar node_modules shallow", "**/node_modules/**", "node_modules/pkg/index.js", true},
		{"doublestar node_modules no match", "**/node_modules/**", "src/app/main.js", false},
		{"doublestar report.xlsx deep", "**/report.xlsx", "a/b/c/report.xlsx", true},
		{"doublestar report.xlsx root", "**/report.xlsx", "report.xlsx", true},
		{"doublestar report.xlsx wrong name", "**/report.xlsx", "a/b/summary.xlsx", false},

		// Base-name globs (no `/`): match at any depth by base name.
		{"basename txt at depth", "*.txt", "src/docs/doc1.txt", true},
		{"basename txt at root", "*.txt", "doc1.txt", true},
		{"basename txt wrong ext", "*.txt", "src/docs/doc1.md", false},
		{"basename exact", "report.xlsx", "deep/dir/report.xlsx", true},

		// Path globs with a single `*` (must NOT cross a boundary).
		{"docs star single level", "docs/*", "docs/file.txt", true},
		{"docs star not deep", "docs/*", "docs/sub/file.txt", false},

		// Exact full-path match.
		{"exact full path", "src/docs/doc1.txt", "src/docs/doc1.txt", true},
		{"exact full path mismatch", "src/docs/doc1.txt", "src/docs/doc2.txt", false},

		// Legacy compatibility: patterns without `**` behave like before.
		{"legacy star-slash-star", "a/*/c.txt", "a/b/c.txt", true},
		{"legacy no match across dirs", "a/*/c.txt", "a/b/x/c.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Match(tc.pattern, tc.relPath)
			if err != nil {
				t.Fatalf("Match(%q, %q) unexpected error: %v", tc.pattern, tc.relPath, err)
			}
			if got != tc.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.relPath, got, tc.want)
			}
		})
	}
}

func TestMatchOSSeparatorNormalized(t *testing.T) {
	// Paths are joined with filepath.Join (OS separator) by callers; ToSlash
	// normalizes them so slash-written patterns match on every platform. Build
	// the path with filepath.Join so this test is meaningful on Windows and a
	// harmless no-op on POSIX.
	rel := filepath.Join("a", "b", "report.xlsx")
	got, err := Match("**/report.xlsx", rel)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("expected OS-separator path to be normalized and match, rel=%q", rel)
	}
}

func TestMatchInvalidPattern(t *testing.T) {
	// An unterminated character class is a bad pattern; Match reports the error
	// and does not match (never panics/aborts).
	got, err := Match("[", "anything")
	if err == nil {
		t.Fatalf("expected error for invalid pattern")
	}
	if got {
		t.Errorf("invalid pattern must not match")
	}
}

func TestMatchAny(t *testing.T) {
	pats := []string{"**/node_modules/**", "*.log"}
	if ok, _ := MatchAny(pats, "src/node_modules/a.js"); !ok {
		t.Errorf("expected node_modules path to match")
	}
	if ok, _ := MatchAny(pats, "deep/app.log"); !ok {
		t.Errorf("expected .log basename to match")
	}
	if ok, _ := MatchAny(pats, "src/main.go"); ok {
		t.Errorf("expected main.go not to match")
	}
	if ok, bad := MatchAny([]string{"["}, "x"); ok || bad != "[" {
		t.Errorf("expected bad pattern reported, got ok=%v bad=%q", ok, bad)
	}
	if ok, _ := MatchAny(nil, "anything"); ok {
		t.Errorf("empty patterns must not match")
	}
}
