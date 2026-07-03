package chunker

import (
	"bytes"
	"crypto/sha256"
	"math/rand"
	"testing"
)

// deterministicData returns n bytes of pseudo-random-but-reproducible data.
func deterministicData(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	r.Read(b)
	return b
}

func chunkAll(t *testing.T, data, key []byte) [][32]byte {
	t.Helper()
	c, err := New(bytes.NewReader(data), key)
	if err != nil {
		t.Fatal(err)
	}
	var hashes [][32]byte
	var total int
	err = c.Split(func(chunk []byte) error {
		total += len(chunk)
		hashes = append(hashes, sha256.Sum256(chunk))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != len(data) {
		t.Fatalf("chunker dropped bytes: got %d, want %d", total, len(data))
	}
	return hashes
}

func TestChunkerDeterministic(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	data := deterministicData(20*1024*1024, 1) // 20 MiB

	a := chunkAll(t, data, key)
	b := chunkAll(t, data, key)

	if len(a) != len(b) {
		t.Fatalf("chunk count differs across runs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("chunk %d differs across runs", i)
		}
	}
	if len(a) < 2 {
		t.Fatalf("expected multiple chunks for 20 MiB, got %d", len(a))
	}
}

func TestChunkerKeyAffectsBoundaries(t *testing.T) {
	data := deterministicData(20*1024*1024, 2)
	k1 := bytes.Repeat([]byte{0x01}, 32)
	k2 := bytes.Repeat([]byte{0x02}, 32)

	a := chunkAll(t, data, k1)
	b := chunkAll(t, data, k2)

	// With different keys the Gear table differs, so boundaries (and thus the
	// chunk hash sequence) should differ for realistic data.
	same := len(a) == len(b)
	if same {
		for i := range a {
			if a[i] != b[i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Fatal("different chunker keys produced identical boundaries")
	}
}

func TestChunkSizeBounds(t *testing.T) {
	key := bytes.Repeat([]byte{0x7}, 32)
	data := deterministicData(30*1024*1024, 3)
	c, err := New(bytes.NewReader(data), key)
	if err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	err = c.Split(func(chunk []byte) error {
		cp := make([]byte, len(chunk))
		copy(cp, chunk)
		chunks = append(chunks, cp)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, ch := range chunks {
		// The final chunk may be smaller than MinSize; interior chunks must
		// respect [MinSize, MaxSize].
		if len(ch) > MaxSize {
			t.Fatalf("chunk %d exceeds MaxSize: %d", i, len(ch))
		}
		if i < len(chunks)-1 && len(ch) < MinSize {
			t.Fatalf("interior chunk %d below MinSize: %d", i, len(ch))
		}
	}
}

func TestChunkerRequiresKey(t *testing.T) {
	if _, err := New(bytes.NewReader([]byte("x")), nil); err == nil {
		t.Fatal("expected error for nil key")
	}
}
