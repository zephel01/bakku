package gdrive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gdrive://backups/prod", "backups/prod"},
		{"gdrive://backups", "backups"},
		{"gdrive://", ""},
		{"backups/prod", "backups/prod"},
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

// fakeDrive is an in-memory implementation of driveAPI simulating a Drive
// folder/file tree, with no network access.
type fakeDrive struct {
	mu       sync.Mutex
	nextID   int
	children map[string][]driveEntry // parentID -> children
	content  map[string][]byte       // fileID -> content
}

func newFakeDrive() *fakeDrive {
	return &fakeDrive{
		children: map[string][]driveEntry{"root": nil},
		content:  map[string][]byte{},
	}
}

func (f *fakeDrive) newID() string {
	f.nextID++
	return fmt.Sprintf("id-%d", f.nextID)
}

func (f *fakeDrive) CreateFile(ctx context.Context, name, parentID string, isFolder bool, content io.Reader) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.newID()
	var size int64
	if content != nil {
		data, err := io.ReadAll(content)
		if err != nil {
			return "", err
		}
		f.content[id] = data
		size = int64(len(data))
	}
	f.children[parentID] = append(f.children[parentID], driveEntry{ID: id, Name: name, IsFolder: isFolder, Size: size})
	if isFolder {
		if _, ok := f.children[id]; !ok {
			f.children[id] = nil
		}
	}
	return id, nil
}

func (f *fakeDrive) FindChild(ctx context.Context, name, parentID string, isFolder bool) (string, int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.children[parentID] {
		if e.Name == name && e.IsFolder == isFolder {
			return e.ID, e.Size, true, nil
		}
	}
	return "", 0, false, nil
}

func (f *fakeDrive) Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error) {
	f.mu.Lock()
	data, ok := f.content[id]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("gdrive: file not found")
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	data = data[offset:]
	if length >= 0 && length < int64(len(data)) {
		data = data[:length]
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeDrive) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.content, id)
	for parent, kids := range f.children {
		out := kids[:0]
		for _, k := range kids {
			if k.ID != id {
				out = append(out, k)
			}
		}
		f.children[parent] = out
	}
	delete(f.children, id)
	return nil
}

func (f *fakeDrive) List(ctx context.Context, parentID string) ([]driveEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]driveEntry(nil), f.children[parentID]...), nil
}

func newTestBackend(rootPath string) (*GDrive, *fakeDrive) {
	fd := newFakeDrive()
	return newWithAPI(fd, rootPath), fd
}

func TestGDrive_SaveLoadStatListDelete(t *testing.T) {
	g, _ := newTestBackend("bakku-backups")
	ctx := context.Background()

	content := []byte("hello gdrive world testing content")
	if err := g.Save(ctx, "data/ab/obj1", bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("Save: %v", err)
	}

	size, err := g.Stat(ctx, "data/ab/obj1")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", size, len(content))
	}

	rc, err := g.Load(ctx, "data/ab/obj1", 0, -1)
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

	rc, err = g.Load(ctx, "data/ab/obj1", 6, 5)
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

	if err := g.Save(ctx, "data/ab/obj2", strings.NewReader("second"), 6); err != nil {
		t.Fatalf("Save obj2: %v", err)
	}
	if err := g.Save(ctx, "data/cd/obj3", strings.NewReader("third"), 5); err != nil {
		t.Fatalf("Save obj3: %v", err)
	}

	var found []string
	err = g.List(ctx, "data", func(key string, size int64) error {
		found = append(found, key)
		return nil
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("List found %d keys, want 3: %v", len(found), found)
	}

	if err := g.Delete(ctx, "data/ab/obj1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = g.Stat(ctx, "data/ab/obj1")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat after delete: expected ErrNotExist, got %v", err)
	}

	if err := g.Delete(ctx, "data/ab/does-not-exist"); err != nil {
		t.Fatalf("Delete missing key: %v", err)
	}
}

func TestGDrive_LoadNotFound(t *testing.T) {
	g, _ := newTestBackend("bakku-backups")
	_, err := g.Load(context.Background(), "missing/key", 0, -1)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestGDrive_StatNotFound(t *testing.T) {
	g, _ := newTestBackend("bakku-backups")
	_, err := g.Stat(context.Background(), "missing/key")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestGDrive_ListMissingPrefixIsEmptyNotError(t *testing.T) {
	g, _ := newTestBackend("bakku-backups")
	var found []string
	err := g.List(context.Background(), "no/such/prefix", func(key string, size int64) error {
		found = append(found, key)
		return nil
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("expected 0 results, got %v", found)
	}
}

func TestSplitKey(t *testing.T) {
	cases := []struct {
		in       string
		wantDirs []string
		wantName string
	}{
		{"data/ab/cd", []string{"data", "ab"}, "cd"},
		{"file", nil, "file"},
		{"/leading/slash/file", []string{"leading", "slash"}, "file"},
	}
	for _, c := range cases {
		dirs, name := splitKey(c.in)
		if name != c.wantName || !equalSlices(dirs, c.wantDirs) {
			t.Fatalf("splitKey(%q) = (%v, %q), want (%v, %q)", c.in, dirs, name, c.wantDirs, c.wantName)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNew_MissingCredentialsEnv(t *testing.T) {
	t.Setenv("BAKKU_GDRIVE_CREDENTIALS", "")
	_, err := New(context.Background(), "gdrive://backups")
	if err == nil {
		t.Fatal("expected error when BAKKU_GDRIVE_CREDENTIALS is unset")
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

var _ = isGoogleAPINotFound
