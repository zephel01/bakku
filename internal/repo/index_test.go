package repo

import (
	"context"
	"testing"

	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/crypto"
)

func TestIndexAddHasLookup(t *testing.T) {
	key, _ := crypto.NewMasterKey()
	ix := newIndex(crypto.DeriveSubKey(key, "ix"))

	loc := IndexLocation{PackID: "pack1", Offset: 10, Length: 20, Type: BlobData, UncompressedLen: 30}
	ix.Add("blob1", loc)
	if !ix.Has("blob1") {
		t.Fatal("Has returned false for added blob")
	}
	got, ok := ix.Lookup("blob1")
	if !ok || got != loc {
		t.Fatalf("Lookup mismatch: %+v", got)
	}
	if ix.Has("missing") {
		t.Fatal("Has true for missing blob")
	}
	// Re-adding must not overwrite.
	ix.Add("blob1", IndexLocation{PackID: "other"})
	got, _ = ix.Lookup("blob1")
	if got.PackID != "pack1" {
		t.Fatal("Add overwrote existing entry")
	}
	if ix.Len() != 1 {
		t.Fatalf("Len = %d, want 1", ix.Len())
	}
}

func TestIndexSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	be, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.NewMasterKey()
	ixKey := crypto.DeriveSubKey(key, "ix")

	ix := newIndex(ixKey)
	ix.Add("a", IndexLocation{PackID: "p1", Offset: 1, Length: 2, UncompressedLen: 3})
	ix.Add("b", IndexLocation{PackID: "p2", Offset: 4, Length: 5, Type: BlobTree, UncompressedLen: 6})
	if _, err := ix.Save(ctx, be); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadIndex(ctx, be, ixKey)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Len() != 2 {
		t.Fatalf("loaded Len = %d, want 2", loaded.Len())
	}
	la, _ := loaded.Lookup("a")
	if la.PackID != "p1" || la.Offset != 1 {
		t.Fatalf("blob a mismatch: %+v", la)
	}
	lb, _ := loaded.Lookup("b")
	if lb.Type != BlobTree || lb.UncompressedLen != 6 {
		t.Fatalf("blob b mismatch: %+v", lb)
	}
}
