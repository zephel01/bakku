package repo

import (
	"bytes"
	"context"
	"testing"

	"github.com/zephel01/bakku/internal/backend/local"
)

func TestRepoInitOpenWrongPassword(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)
	if _, err := Init(ctx, be, []byte("correct-horse")); err != nil {
		t.Fatal(err)
	}

	be2, _ := local.New(dir)
	if _, err := Open(ctx, be2, []byte("wrong-password")); err != ErrWrongPassword {
		t.Fatalf("expected ErrWrongPassword, got %v", err)
	}

	be3, _ := local.New(dir)
	if _, err := Open(ctx, be3, []byte("correct-horse")); err != nil {
		t.Fatalf("open with correct password failed: %v", err)
	}
}

func TestRepoDoubleInitFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)
	if _, err := Init(ctx, be, []byte("pw")); err != nil {
		t.Fatal(err)
	}
	be2, _ := local.New(dir)
	if _, err := Init(ctx, be2, []byte("pw")); err == nil {
		t.Fatal("expected double-init to fail")
	}
}

func TestRepoBlobRoundTripAndDedup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)
	r, err := Init(ctx, be, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}

	data := bytes.Repeat([]byte("hello world "), 1000)
	id, isNew, err := r.SaveBlob(ctx, BlobData, data)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew {
		t.Fatal("first SaveBlob should report isNew=true")
	}
	// Same content again -> dedup.
	id2, isNew2, err := r.SaveBlob(ctx, BlobData, data)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id || isNew2 {
		t.Fatalf("expected dedup: id2=%s isNew2=%v", id2, isNew2)
	}

	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	// Reopen and read the blob back.
	be2, _ := local.New(dir)
	r2, err := Open(ctx, be2, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if !r2.HasBlob(id) {
		t.Fatal("reopened repo missing blob in index")
	}
	got, err := r2.LoadBlob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("blob round trip mismatch")
	}
}

func TestRepoTreeAndSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)
	r, _ := Init(ctx, be, []byte("pw"))

	treeID, err := r.SaveTree(ctx, []Node{{Name: "x", Type: NodeFile, Content: []string{"c1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snapID, err := r.SaveSnapshot(ctx, &Snapshot{Paths: []string{"/x"}, Tree: treeID})
	if err != nil {
		t.Fatal(err)
	}

	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].ID != snapID {
		t.Fatalf("ListSnapshots wrong: %+v", snaps)
	}
	found, err := r.FindSnapshot(ctx, snapID[:6])
	if err != nil {
		t.Fatal(err)
	}
	if found.Tree != treeID {
		t.Fatal("FindSnapshot returned wrong tree")
	}
	nodes, err := r.LoadTree(ctx, treeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "x" {
		t.Fatalf("LoadTree wrong: %+v", nodes)
	}
}
