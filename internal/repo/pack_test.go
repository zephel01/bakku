package repo

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/zephel01/bakku/internal/crypto"
)

func TestPackWriteReadRoundTrip(t *testing.T) {
	key, _ := crypto.NewMasterKey()
	dataKey := crypto.DeriveSubKey(key, "test-data-key")

	pw, err := newPackWriter(dataKey)
	if err != nil {
		t.Fatal(err)
	}
	defer pw.close()

	blobs := [][]byte{
		[]byte("first blob content"),
		bytes.Repeat([]byte("A"), 4096),
		[]byte(""),
		[]byte("last blob"),
	}
	var entries []blobEntry
	for _, b := range blobs {
		h := crypto.HashChunk(b)
		e, err := pw.addBlob(h, BlobData, b)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}

	packID, packBytes, hdrEntries, err := pw.finalize()
	if err != nil {
		t.Fatal(err)
	}
	if packID == "" {
		t.Fatal("empty pack id")
	}
	if len(hdrEntries) != len(blobs) {
		t.Fatalf("header entry count = %d, want %d", len(hdrEntries), len(blobs))
	}

	// Read each blob back by offset/length and verify plaintext + id.
	for i, e := range entries {
		sealed := packBytes[e.Offset : e.Offset+e.Length]
		plain, err := decodeBlob(dataKey, sealed)
		if err != nil {
			t.Fatalf("blob %d decode: %v", i, err)
		}
		if !bytes.Equal(plain, blobs[i]) {
			t.Fatalf("blob %d mismatch: got %q want %q", i, plain, blobs[i])
		}
		h := crypto.HashChunk(blobs[i])
		if e.ID != hex.EncodeToString(h[:]) {
			t.Fatalf("blob %d id mismatch", i)
		}
	}

	// The trailing header must parse back to the same entries.
	parsed, err := readPackHeader(dataKey, packBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(entries) {
		t.Fatalf("readPackHeader count = %d, want %d", len(parsed), len(entries))
	}
	for i := range parsed {
		if parsed[i] != entries[i] {
			t.Fatalf("header entry %d mismatch: %+v vs %+v", i, parsed[i], entries[i])
		}
	}
}

func TestPackKeyLayout(t *testing.T) {
	if got := PackKey("ab12cd"); got != "data/ab/ab12cd" {
		t.Fatalf("PackKey = %q", got)
	}
}
