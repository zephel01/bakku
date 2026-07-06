package repo

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/klauspost/compress/zstd"
	"github.com/zephel01/bakku/internal/crypto"
)

// BlobType distinguishes data blobs (file content) from tree blobs (directory
// metadata) in the index and pack header.
type BlobType uint8

const (
	// BlobData is a chunk of file content.
	BlobData BlobType = iota
	// BlobTree is a serialized directory tree node.
	BlobTree
)

func (t BlobType) String() string {
	switch t {
	case BlobData:
		return "data"
	case BlobTree:
		return "tree"
	default:
		return "unknown"
	}
}

// packTargetSize is the soft target for a pack file (~16 MiB). A pack is
// flushed once it reaches this size.
const packTargetSize = 16 * 1024 * 1024

// blobEntry describes one blob's location within a pack. UncompressedLen is the
// plaintext (pre-compression) length; Length is the on-disk encrypted length.
type blobEntry struct {
	ID              string   `json:"id"`   // hex blob id
	Type            BlobType `json:"type"`
	Offset          int64    `json:"off"`
	Length          int64    `json:"len"`  // encrypted+compressed length in pack
	UncompressedLen int64    `json:"ulen"` // plaintext length
}

// packWriter accumulates encrypted blobs into an in-memory buffer and, on
// Finalize, appends an encrypted header (blob list) followed by a uint32 header
// length. Each blob is: zstd-compress(plaintext) -> AES-GCM(nonce||ct).
type packWriter struct {
	buf     bytes.Buffer
	entries []blobEntry
	dataKey []byte // subkey for encrypting blobs and the header
	enc     *zstd.Encoder
}

func newPackWriter(dataKey []byte) (*packWriter, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	return &packWriter{dataKey: dataKey, enc: enc}, nil
}

// addBlob compresses+encrypts plaintext and appends it to the pack. It returns
// the recorded blobEntry. id must be the BLAKE3 hash of the plaintext.
func (w *packWriter) addBlob(id [32]byte, typ BlobType, plaintext []byte) (blobEntry, error) {
	compressed := w.enc.EncodeAll(plaintext, nil)
	sealed, err := crypto.Seal(w.dataKey, compressed, nil)
	if err != nil {
		return blobEntry{}, err
	}
	off := int64(w.buf.Len())
	if _, err := w.buf.Write(sealed); err != nil {
		return blobEntry{}, err
	}
	e := blobEntry{
		ID:              hex.EncodeToString(id[:]),
		Type:            typ,
		Offset:          off,
		Length:          int64(len(sealed)),
		UncompressedLen: int64(len(plaintext)),
	}
	w.entries = append(w.entries, e)
	return e, nil
}

// size returns the current data-section size (excluding the not-yet-written
// header).
func (w *packWriter) size() int64 { return int64(w.buf.Len()) }

// empty reports whether no blobs have been added.
func (w *packWriter) empty() bool { return len(w.entries) == 0 }

// finalize appends the encrypted header and returns (packID, bytes, entries).
// The packID is the BLAKE3 hash of the data+header section (content addressed).
func (w *packWriter) finalize() (string, []byte, []blobEntry, error) {
	headerJSON, err := json.Marshal(w.entries)
	if err != nil {
		return "", nil, nil, err
	}
	sealedHeader, err := crypto.Seal(w.dataKey, headerJSON, nil)
	if err != nil {
		return "", nil, nil, err
	}
	if _, err := w.buf.Write(sealedHeader); err != nil {
		return "", nil, nil, err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(sealedHeader)))
	if _, err := w.buf.Write(lenBuf[:]); err != nil {
		return "", nil, nil, err
	}
	packBytes := w.buf.Bytes()
	id := crypto.HashChunk(packBytes)
	packID := hex.EncodeToString(id[:])
	// Copy out so the caller owns the bytes independent of the buffer.
	out := make([]byte, len(packBytes))
	copy(out, packBytes)
	return packID, out, w.entries, nil
}

// reset clears the writer for reuse.
func (w *packWriter) reset() {
	w.buf.Reset()
	w.entries = w.entries[:0]
}

func (w *packWriter) close() error {
	if w.enc != nil {
		w.enc.Close()
	}
	return nil
}

// PackKey returns the storage key ("data/<xx>/<packID>") for a pack id.
func PackKey(packID string) string {
	if len(packID) < 2 {
		return "data/00/" + packID
	}
	return "data/" + packID[:2] + "/" + packID
}

// --- pack reading ---

// maxDecompressedBlobSize caps the decoded size of any single blob to bound
// memory use. A blob is at most one content chunk (chunker max 8 MiB) or a
// tree/snapshot JSON; 512 MiB is far above any legitimate blob yet stops a
// zstd "decompression bomb" — a small, validly-encrypted ciphertext that
// expands to many gigabytes — from exhausting memory during restore/verify.
const maxDecompressedBlobSize = 512 << 20 // 512 MiB

// zstd decoder shared for reads; safe for concurrent use.
var packDecoder *zstd.Decoder

func init() {
	d, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecompressedBlobSize))
	if err != nil {
		panic(err)
	}
	packDecoder = d
}

// validBlobRange reports whether a blob's recorded offset/length are
// self-consistent: non-negative offset, positive length, and no int64 overflow
// when summed. When the containing pack size is known, pass it as packLen (>= 0)
// to also confirm the range lies within the pack; pass -1 when the size is not
// available (e.g. a ranged backend read). Offsets/lengths originate from index
// or pack-header data that a malicious storage server could tamper with, so
// every consumer must validate before slicing or issuing a ranged read.
func validBlobRange(offset, length, packLen int64) bool {
	if offset < 0 || length <= 0 {
		return false
	}
	if offset > math.MaxInt64-length { // overflow guard for offset+length
		return false
	}
	if packLen >= 0 && offset+length > packLen {
		return false
	}
	return true
}

// decodeBlob decrypts+decompresses a single sealed blob read from a pack.
func decodeBlob(dataKey, sealed []byte) ([]byte, error) {
	compressed, err := crypto.Open(dataKey, sealed, nil)
	if err != nil {
		return nil, err
	}
	plain, err := packDecoder.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("repo: pack decompress failed: %w", err)
	}
	return plain, nil
}

// readPackHeader reads and decrypts a pack's trailing header given the whole
// pack bytes. Used for repository rebuild/verify.
func readPackHeader(dataKey, packBytes []byte) ([]blobEntry, error) {
	if len(packBytes) < 4 {
		return nil, errors.New("repo: pack too short")
	}
	hlen := binary.BigEndian.Uint32(packBytes[len(packBytes)-4:])
	// Compare in 64-bit to avoid uint32->int narrowing overflow on 32-bit
	// builds (where a header length > MaxInt32 would wrap negative and defeat
	// the bounds check, causing a slice-bounds panic / DoS).
	if uint64(hlen)+4 > uint64(len(packBytes)) {
		return nil, errors.New("repo: pack header length out of range")
	}
	start := len(packBytes) - 4 - int(hlen)
	sealed := packBytes[start : len(packBytes)-4]
	headerJSON, err := crypto.Open(dataKey, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("repo: pack header decrypt failed: %w", err)
	}
	var entries []blobEntry
	if err := json.Unmarshal(headerJSON, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// randomHex returns n random bytes hex-encoded (used for index/snapshot ids).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
