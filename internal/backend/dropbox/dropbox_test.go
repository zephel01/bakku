package dropbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"dropbox://backups/prod", "/backups/prod"},
		{"dropbox://backups", "/backups"},
		{"dropbox://", ""},
		{"backups/prod", "/backups/prod"},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("ParseURL(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// fakeFilesAPI is an in-memory implementation of filesAPI simulating enough
// of Dropbox's Files API to exercise Save (including upload sessions),
// Load, Stat, List, and Delete, with no network access.
type fakeFilesAPI struct {
	mu   sync.Mutex
	data map[string][]byte // path -> content

	sessions      map[string][]byte // sessionID -> accumulated content
	nextSessionID int
}

func newFakeFilesAPI() *fakeFilesAPI {
	return &fakeFilesAPI{
		data:     map[string][]byte{},
		sessions: map[string][]byte{},
	}
}

func (f *fakeFilesAPI) Upload(arg *files.UploadArg, content io.Reader) (*files.FileMetadata, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.data[arg.Path] = data
	f.mu.Unlock()
	return &files.FileMetadata{Metadata: files.Metadata{PathDisplay: arg.Path, Name: baseName(arg.Path)}, Size: uint64(len(data))}, nil
}

func (f *fakeFilesAPI) UploadSessionStart(arg *files.UploadSessionStartArg, content io.Reader) (*files.UploadSessionStartResult, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.nextSessionID++
	id := fmt.Sprintf("sess-%d", f.nextSessionID)
	f.sessions[id] = append([]byte(nil), data...)
	f.mu.Unlock()
	return &files.UploadSessionStartResult{SessionId: id}, nil
}

func (f *fakeFilesAPI) UploadSessionAppendV2(arg *files.UploadSessionAppendArg, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.sessions[arg.Cursor.SessionId]
	if !ok {
		return errors.New("dropbox: unknown session")
	}
	if uint64(len(existing)) != arg.Cursor.Offset {
		return fmt.Errorf("dropbox: offset mismatch: have %d, cursor says %d", len(existing), arg.Cursor.Offset)
	}
	f.sessions[arg.Cursor.SessionId] = append(existing, data...)
	return nil
}

func (f *fakeFilesAPI) UploadSessionFinish(arg *files.UploadSessionFinishArg, content io.Reader) (*files.FileMetadata, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.sessions[arg.Cursor.SessionId]
	if !ok {
		return nil, errors.New("dropbox: unknown session")
	}
	if uint64(len(existing)) != arg.Cursor.Offset {
		return nil, fmt.Errorf("dropbox: offset mismatch on finish: have %d, cursor says %d", len(existing), arg.Cursor.Offset)
	}
	final := append(existing, data...)
	delete(f.sessions, arg.Cursor.SessionId)
	f.data[arg.Commit.Path] = final
	return &files.FileMetadata{Metadata: files.Metadata{PathDisplay: arg.Commit.Path, Name: baseName(arg.Commit.Path)}, Size: uint64(len(final))}, nil
}

func (f *fakeFilesAPI) Download(arg *files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error) {
	f.mu.Lock()
	data, ok := f.data[arg.Path]
	f.mu.Unlock()
	if !ok {
		return nil, nil, errors.New("path/not_found/")
	}
	body := data
	if rng, ok := arg.ExtraHeaders["Range"]; ok {
		var start, end int
		rng = strings.TrimPrefix(rng, "bytes=")
		parts := strings.SplitN(rng, "-", 2)
		fmt.Sscanf(parts[0], "%d", &start)
		if parts[1] == "" {
			end = len(data) - 1
		} else {
			fmt.Sscanf(parts[1], "%d", &end)
		}
		if end >= len(data) {
			end = len(data) - 1
		}
		body = data[start : end+1]
	}
	meta := &files.FileMetadata{Metadata: files.Metadata{PathDisplay: arg.Path, Name: baseName(arg.Path)}, Size: uint64(len(data))}
	return meta, io.NopCloser(strings.NewReader(string(body))), nil
}

func (f *fakeFilesAPI) GetMetadata(arg *files.GetMetadataArg) (files.IsMetadata, error) {
	f.mu.Lock()
	data, ok := f.data[arg.Path]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("path/not_found/")
	}
	return &files.FileMetadata{Metadata: files.Metadata{PathDisplay: arg.Path, Name: baseName(arg.Path)}, Size: uint64(len(data))}, nil
}

func (f *fakeFilesAPI) Delete(arg *files.DeleteArg) (files.IsMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.data[arg.Path]; !ok {
		return nil, errors.New("path_lookup/not_found/")
	}
	delete(f.data, arg.Path)
	return nil, nil
}

func (f *fakeFilesAPI) ListFolder(arg *files.ListFolderArg) (*files.ListFolderResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var entries []files.IsMetadata
	for p, data := range f.data {
		if arg.Path == "" || strings.HasPrefix(p, arg.Path+"/") || p == arg.Path {
			entries = append(entries, &files.FileMetadata{
				Metadata: files.Metadata{PathDisplay: p, Name: baseName(p)},
				Size:     uint64(len(data)),
			})
		}
	}
	return &files.ListFolderResult{Entries: entries, HasMore: false}, nil
}

func (f *fakeFilesAPI) ListFolderContinue(arg *files.ListFolderContinueArg) (*files.ListFolderResult, error) {
	return &files.ListFolderResult{HasMore: false}, nil
}

func baseName(p string) string {
	parts := strings.Split(p, "/")
	return parts[len(parts)-1]
}

func newTestBackend(root string) (*Dropbox, *fakeFilesAPI) {
	fa := newFakeFilesAPI()
	return newWithAPI(fa, root), fa
}

func TestDropbox_SaveLoadStatListDelete(t *testing.T) {
	d, _ := newTestBackend("/bakku-backups")
	ctx := context.Background()

	content := []byte("hello dropbox world testing content")
	if err := d.Save(ctx, "data/ab/obj1", strings.NewReader(string(content)), int64(len(content))); err != nil {
		t.Fatalf("Save: %v", err)
	}

	size, err := d.Stat(ctx, "data/ab/obj1")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", size, len(content))
	}

	rc, err := d.Load(ctx, "data/ab/obj1", 0, -1)
	if err != nil {
		t.Fatalf("Load full: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("Load full = %q, want %q", got, content)
	}

	rc, err = d.Load(ctx, "data/ab/obj1", 6, 5)
	if err != nil {
		t.Fatalf("Load range: %v", err)
	}
	got, err = io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll range: %v", err)
	}
	want := content[6:11]
	if string(got) != string(want) {
		t.Fatalf("Load range = %q, want %q", got, want)
	}

	if err := d.Save(ctx, "data/ab/obj2", strings.NewReader("second"), 6); err != nil {
		t.Fatalf("Save obj2: %v", err)
	}

	var found []string
	err = d.List(ctx, "data", func(key string, size int64) error {
		found = append(found, key)
		return nil
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("List found %d keys, want 2: %v", len(found), found)
	}

	if err := d.Delete(ctx, "data/ab/obj1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = d.Stat(ctx, "data/ab/obj1")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat after delete: expected ErrNotExist-like, got %v", err)
	}

	if err := d.Delete(ctx, "data/ab/does-not-exist"); err != nil {
		t.Fatalf("Delete missing key: %v", err)
	}
}

func TestDropbox_LoadNotFound(t *testing.T) {
	d, _ := newTestBackend("/bakku-backups")
	_, err := d.Load(context.Background(), "missing/key", 0, -1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not exist") {
		t.Fatalf("expected not-exist-ish error, got %v", err)
	}
}

func TestDropbox_StatNotFound(t *testing.T) {
	d, _ := newTestBackend("/bakku-backups")
	_, err := d.Stat(context.Background(), "missing/key")
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestDropbox_SaveUploadSession forces the upload-session chunking path (by
// shrinking the threshold/chunk-size vars for the duration of the test) and
// verifies a payload spanning multiple chunks round-trips correctly.
func TestDropbox_SaveUploadSession(t *testing.T) {
	origThreshold, origChunk := uploadSessionThreshold, uploadChunkSize
	uploadSessionThreshold = 10
	uploadChunkSize = 4
	t.Cleanup(func() {
		uploadSessionThreshold = origThreshold
		uploadChunkSize = origChunk
	})

	d, fa := newTestBackend("/bakku-backups")
	ctx := context.Background()

	// 22 bytes, well over the shrunk 10-byte threshold, spanning multiple
	// 4-byte chunks (4+4+4+4+4+2).
	content := []byte("0123456789abcdefghijkl")
	if err := d.Save(ctx, "big/obj", strings.NewReader(string(content)), int64(len(content))); err != nil {
		t.Fatalf("Save (session): %v", err)
	}

	fa.mu.Lock()
	stored, ok := fa.data["/bakku-backups/big/obj"]
	fa.mu.Unlock()
	if !ok {
		t.Fatal("expected file to be stored via upload session")
	}
	if string(stored) != string(content) {
		t.Fatalf("stored content = %q, want %q", stored, content)
	}

	size, err := d.Stat(ctx, "big/obj")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", size, len(content))
	}
}

// TestDropbox_SaveUploadSession_ExactChunkMultiple exercises the edge case
// where the payload length is an exact multiple of the chunk size, so the
// final read returns 0 bytes at EOF.
func TestDropbox_SaveUploadSession_ExactChunkMultiple(t *testing.T) {
	origThreshold, origChunk := uploadSessionThreshold, uploadChunkSize
	uploadSessionThreshold = 4
	uploadChunkSize = 4
	t.Cleanup(func() {
		uploadSessionThreshold = origThreshold
		uploadChunkSize = origChunk
	})

	d, fa := newTestBackend("/bakku-backups")
	ctx := context.Background()

	content := []byte("01234567") // exactly 2 chunks of 4
	if err := d.Save(ctx, "exact/obj", strings.NewReader(string(content)), int64(len(content))); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fa.mu.Lock()
	stored := fa.data["/bakku-backups/exact/obj"]
	fa.mu.Unlock()
	if string(stored) != string(content) {
		t.Fatalf("stored = %q, want %q", stored, content)
	}
}

func TestNew_MissingTokenEnv(t *testing.T) {
	t.Setenv("BAKKU_DROPBOX_TOKEN", "")
	_, err := New(context.Background(), "dropbox://backups")
	if err == nil {
		t.Fatal("expected error when BAKKU_DROPBOX_TOKEN is unset")
	}
}

func TestRangeHeader(t *testing.T) {
	cases := []struct {
		offset, length int64
		want           string
	}{
		{0, -1, ""},
		{5, -1, "bytes=5-"},
		{5, 10, "bytes=5-14"},
	}
	for _, c := range cases {
		got := rangeHeader(c.offset, c.length)
		if got != c.want {
			t.Fatalf("rangeHeader(%d,%d) = %q, want %q", c.offset, c.length, got, c.want)
		}
	}
}
