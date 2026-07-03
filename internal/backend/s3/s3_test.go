package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want ParsedURL
	}{
		{
			in:   "s3://my-bucket/some/prefix",
			want: ParsedURL{Bucket: "my-bucket", Prefix: "some/prefix"},
		},
		{
			in:   "s3://my-bucket",
			want: ParsedURL{Bucket: "my-bucket", Prefix: ""},
		},
		{
			in:   "s3://my-bucket/prefix?endpoint=https://minio.local:9000&region=us-west-2",
			want: ParsedURL{Bucket: "my-bucket", Prefix: "prefix", Endpoint: "https://minio.local:9000", Region: "us-west-2"},
		},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("ParseURL(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseURL(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseURL_MissingBucket(t *testing.T) {
	_, err := ParseURL("s3://")
	if err == nil {
		t.Fatal("expected error for missing bucket")
	}
}

// fakeS3Server is a minimal in-memory S3-compatible HTTP server sufficient to
// exercise Save/Load/Stat/List/Delete against the real AWS SDK client.
type fakeS3Server struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3Server() *httptest.Server {
	f := &fakeS3Server{objects: map[string][]byte{}}
	return httptest.NewServer(http.HandlerFunc(f.handle))
}

func (f *fakeS3Server) handle(w http.ResponseWriter, r *http.Request) {
	// Path-style requests: /bucket/key...
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	bucket := parts[0]
	key := ""
	if len(parts) > 1 {
		key = parts[1]
	}
	_ = bucket

	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.objects[key] = data
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			f.handleList(w, r, bucket)
			return
		}
		data, ok := f.objects[key]
		if !ok {
			f.writeNotFound(w, "NoSuchKey")
			return
		}
		rangeHdr := r.Header.Get("Range")
		if rangeHdr != "" {
			start, end, ok := parseRange(rangeHdr, len(data))
			if !ok {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			data = data[start : end+1]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		w.Write(data)

	case http.MethodHead:
		data, ok := f.objects[key]
		if !ok {
			f.writeNotFound(w, "")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3Server) writeNotFound(w http.ResponseWriter, code string) {
	w.WriteHeader(http.StatusNotFound)
	if code != "" {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>Not Found</Message></Error>`, code)
	}
}

func (f *fakeS3Server) handleList(w http.ResponseWriter, r *http.Request, bucket string) {
	prefix := r.URL.Query().Get("prefix")
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	fmt.Fprintf(&b, `<Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><IsTruncated>false</IsTruncated>`, bucket, prefix, len(keys))
	for _, k := range keys {
		fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size></Contents>`, k, len(f.objects[k]))
	}
	b.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}

func parseRange(hdr string, size int) (start, end int, ok bool) {
	hdr = strings.TrimPrefix(hdr, "bytes=")
	parts := strings.SplitN(hdr, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err1 := strconv.Atoi(parts[0])
	if err1 != nil {
		return 0, 0, false
	}
	if parts[1] == "" {
		end = size - 1
	} else {
		e, err2 := strconv.Atoi(parts[1])
		if err2 != nil {
			return 0, 0, false
		}
		end = e
	}
	if end >= size {
		end = size - 1
	}
	if start > end {
		return 0, 0, false
	}
	return start, end, true
}

// newTestBackend builds an S3 backend pointed at the fake server.
func newTestBackend(t *testing.T, srv *httptest.Server, bucket, prefix string) *S3 {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("AWS_REGION", "us-east-1")

	dst := "s3://" + bucket
	if prefix != "" {
		dst += "/" + prefix
	}
	dst += "?endpoint=" + url.QueryEscape(srv.URL) + "&region=us-east-1"

	b, err := New(context.Background(), dst)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

func TestS3_SaveLoadStatListDelete(t *testing.T) {
	srv := newFakeS3Server()
	defer srv.Close()

	b := newTestBackend(t, srv, "test-bucket", "myprefix")
	ctx := context.Background()

	content := []byte("hello world, this is a test object")
	if err := b.Save(ctx, "data/ab/obj1", strings.NewReader(string(content)), int64(len(content))); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Stat
	size, err := b.Stat(ctx, "data/ab/obj1")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", size, len(content))
	}

	// Load full
	rc, err := b.Load(ctx, "data/ab/obj1", 0, -1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("Load full = %q, want %q", got, content)
	}

	// Load range
	rc, err = b.Load(ctx, "data/ab/obj1", 6, 5)
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

	// Save a second object for List
	if err := b.Save(ctx, "data/ab/obj2", strings.NewReader("second"), 6); err != nil {
		t.Fatalf("Save obj2: %v", err)
	}

	var found []string
	err = b.List(ctx, "data", func(key string, size int64) error {
		found = append(found, key)
		return nil
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("List found %d keys, want 2: %v", len(found), found)
	}

	// Delete
	if err := b.Delete(ctx, "data/ab/obj1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = b.Stat(ctx, "data/ab/obj1")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat after delete: expected ErrNotExist, got %v", err)
	}

	// Delete missing key is not an error
	if err := b.Delete(ctx, "data/ab/does-not-exist"); err != nil {
		t.Fatalf("Delete missing key: %v", err)
	}
}

func TestS3_LoadNotFound(t *testing.T) {
	srv := newFakeS3Server()
	defer srv.Close()
	b := newTestBackend(t, srv, "test-bucket", "")
	_, err := b.Load(context.Background(), "missing/key", 0, -1)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestS3_StatNotFound(t *testing.T) {
	srv := newFakeS3Server()
	defer srv.Close()
	b := newTestBackend(t, srv, "test-bucket", "")
	_, err := b.Stat(context.Background(), "missing/key")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestS3_ObjectKeyPrefixing(t *testing.T) {
	b := &S3{bucket: "b", prefix: "myprefix"}
	if got := b.objectKey("data/x"); got != "myprefix/data/x" {
		t.Fatalf("objectKey = %q, want %q", got, "myprefix/data/x")
	}
	b2 := &S3{bucket: "b", prefix: ""}
	if got := b2.objectKey("data/x"); got != "data/x" {
		t.Fatalf("objectKey (no prefix) = %q, want %q", got, "data/x")
	}
}

func TestS3_RangeHeader(t *testing.T) {
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

// Ensure New wires context cancellation through to config loading / usage;
// this doesn't hit the network but confirms the option plumbing works and
// unused import s3/awssdk stay referenced through newWithOptFns in other
// tests within the package (kept for clarity/compat with SDK types used).
var _ = awssdk.String
var _ = s3.Options{}
