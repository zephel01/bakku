package repo

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/crypto"
)

// Snapshot is the root record of one backup run. It is stored (encrypted) under
// snapshots/<id>, where id is the BLAKE3 hash of the plaintext snapshot JSON.
type Snapshot struct {
	Version  int       `json:"version"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags,omitempty"`
	Tree     string    `json:"tree"` // root tree blob id

	// ID is the snapshot id; populated on load/save, not part of the hashed JSON.
	ID string `json:"-"`
}

// SnapshotKey returns the storage key for a snapshot id.
func SnapshotKey(id string) string { return "snapshots/" + id }

// saveSnapshot serializes, hashes, encrypts and stores a snapshot. It returns
// the snapshot id (hash of the plaintext JSON).
func saveSnapshot(ctx context.Context, be backend.Backend, key []byte, snap *Snapshot) (string, error) {
	plain, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	h := crypto.HashChunk(plain)
	id := hexEncode(h[:])
	sealed, err := crypto.Seal(key, plain, nil)
	if err != nil {
		return "", err
	}
	if err := be.Save(ctx, SnapshotKey(id), bytesReader(sealed), int64(len(sealed))); err != nil {
		return "", err
	}
	snap.ID = id
	return id, nil
}

// loadSnapshot reads and decrypts a single snapshot by id.
func loadSnapshot(ctx context.Context, be backend.Backend, key []byte, id string) (*Snapshot, error) {
	blob, err := readAll(ctx, be, SnapshotKey(id))
	if err != nil {
		return nil, err
	}
	plain, err := crypto.Open(key, blob, nil)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(plain, &snap); err != nil {
		return nil, err
	}
	snap.ID = id
	return &snap, nil
}

// listSnapshots loads all snapshots, sorted newest-first.
func listSnapshots(ctx context.Context, be backend.Backend, key []byte) ([]*Snapshot, error) {
	var snaps []*Snapshot
	err := be.List(ctx, "snapshots", func(k string, _ int64) error {
		id := k[len("snapshots/"):]
		s, err := loadSnapshot(ctx, be, key, id)
		if err != nil {
			return err
		}
		snaps = append(snaps, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].Time.After(snaps[j].Time)
	})
	return snaps, nil
}
