package repo

import (
	"testing"
	"time"
)

func TestTreeMarshalRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	nodes := []Node{
		{Name: "file.txt", Type: NodeFile, Mode: 0o644, ModTime: now, Size: 42, Content: []string{"a", "b"}},
		{Name: "sub", Type: NodeDir, Mode: 0o755, ModTime: now, Subtree: "treeid"},
		{Name: "link", Type: NodeSymlink, Mode: 0o777, ModTime: now, LinkTarget: "../target"},
	}
	b, err := marshalTree(nodes)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalTree(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(nodes) {
		t.Fatalf("node count = %d, want %d", len(got), len(nodes))
	}
	for i := range nodes {
		if got[i].Name != nodes[i].Name || got[i].Type != nodes[i].Type ||
			got[i].Mode != nodes[i].Mode || got[i].Size != nodes[i].Size ||
			got[i].Subtree != nodes[i].Subtree || got[i].LinkTarget != nodes[i].LinkTarget {
			t.Fatalf("node %d mismatch: %+v vs %+v", i, got[i], nodes[i])
		}
		if !got[i].ModTime.Equal(nodes[i].ModTime) {
			t.Fatalf("node %d mtime mismatch", i)
		}
	}
	// Determinism: same input marshals identically.
	b2, _ := marshalTree(nodes)
	if string(b) != string(b2) {
		t.Fatal("tree marshaling not deterministic")
	}
}
