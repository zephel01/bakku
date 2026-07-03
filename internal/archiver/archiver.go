// Package archiver walks the filesystem and writes files/directories into a
// repository as a snapshot, deduplicating unchanged chunks against the existing
// index. The heavy per-file work (chunk -> dedup -> compress -> encrypt -> pack)
// is parallelized across worker goroutines with errgroup.
package archiver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/zephel01/bakku/internal/chunker"
	bfs "github.com/zephel01/bakku/internal/fs"
	"github.com/zephel01/bakku/internal/repo"
)

// Stats summarizes a backup run.
type Stats struct {
	FilesNew     int64
	Dirs         int64
	Symlinks     int64
	ChunksNew    int64
	ChunksReused int64
	BytesTotal   int64 // total plaintext bytes read from files
	BytesNew     int64 // plaintext bytes of newly stored chunks
}

// Options configure a backup run.
type Options struct {
	Paths    []string // absolute or relative paths to back up
	Tags     []string
	Excludes []string // glob patterns (matched against base name and full path)
	Parallel int      // worker goroutines; <=0 uses GOMAXPROCS
}

// Archiver runs backups against a repository.
type Archiver struct {
	repo     *repo.Repository
	chunkKey []byte

	// linkMu guards linkSeen, the hard-link registry for the in-progress
	// Backup call: the first node observed for a given (dev, inode) is stored
	// by content; subsequent nodes sharing that key are stored as a LinkTo
	// reference to the first one's snapshot-relative path.
	linkMu   sync.Mutex
	linkSeen map[bfs.LinkKey]string
}

// New returns an Archiver bound to r.
func New(r *repo.Repository) *Archiver {
	return &Archiver{repo: r, chunkKey: r.ChunkerKey()}
}

// Backup walks opts.Paths, stores their contents, and creates a snapshot.
// It returns the snapshot id and run statistics.
func (a *Archiver) Backup(ctx context.Context, opts Options) (string, *Stats, error) {
	if len(opts.Paths) == 0 {
		return "", nil, fmt.Errorf("archiver: no paths to back up")
	}
	stats := &Stats{}
	a.linkMu.Lock()
	a.linkSeen = make(map[bfs.LinkKey]string)
	a.linkMu.Unlock()

	// Build the root tree by processing each top-level path. Each path becomes a
	// node in the synthetic root directory, keyed by its base name.
	rootNodes := make([]repo.Node, 0, len(opts.Paths))
	for _, p := range opts.Paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", nil, err
		}
		node, err := a.archivePath(ctx, abs, filepath.Base(abs), opts, stats)
		if err != nil {
			return "", nil, err
		}
		if node != nil {
			rootNodes = append(rootNodes, *node)
		}
	}

	sort.Slice(rootNodes, func(i, j int) bool { return rootNodes[i].Name < rootNodes[j].Name })
	rootTreeID, err := a.repo.SaveTree(ctx, rootNodes)
	if err != nil {
		return "", nil, err
	}

	// Flush packs + index before writing the snapshot so all referenced blobs
	// are durably stored and indexed.
	if err := a.repo.Flush(ctx); err != nil {
		return "", nil, err
	}

	host, _ := os.Hostname()
	absPaths := make([]string, len(opts.Paths))
	for i, p := range opts.Paths {
		absPaths[i], _ = filepath.Abs(p)
	}
	snap := &repo.Snapshot{
		Time:     time.Now().UTC(),
		Hostname: host,
		Paths:    absPaths,
		Tags:     opts.Tags,
		Tree:     rootTreeID,
	}
	id, err := a.repo.SaveSnapshot(ctx, snap)
	if err != nil {
		return "", nil, err
	}
	return id, stats, nil
}

// archivePath returns a tree Node for a single filesystem path (file, dir, or
// symlink), recursing into directories. Returns nil node for excluded/skipped.
// relPath is the snapshot-relative, slash-separated path of this entry, used
// for hard-link target references.
func (a *Archiver) archivePath(ctx context.Context, path, relPath string, opts Options, stats *Stats) (*repo.Node, error) {
	if isExcluded(path, opts.Excludes) {
		return nil, nil
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)

	owner, hasOwner, xattrs, linkKey, hasLink := bfs.Collect(path, fi)

	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		atomic.AddInt64(&stats.Symlinks, 1)
		n := &repo.Node{
			Name:       name,
			Type:       repo.NodeSymlink,
			Mode:       uint32(fi.Mode()),
			ModTime:    fi.ModTime().UTC(),
			LinkTarget: target,
		}
		applyCollectedMeta(n, owner, hasOwner, xattrs)
		return n, nil

	case fi.IsDir():
		child, err := a.archiveDir(ctx, path, relPath, opts, stats)
		if err != nil {
			return nil, err
		}
		atomic.AddInt64(&stats.Dirs, 1)
		n := &repo.Node{
			Name:    name,
			Type:    repo.NodeDir,
			Mode:    uint32(fi.Mode()),
			ModTime: fi.ModTime().UTC(),
			Subtree: child,
		}
		applyCollectedMeta(n, owner, hasOwner, xattrs)
		return n, nil

	case fi.Mode().IsRegular():
		// Hard-link detection: if we have already archived a file sharing this
		// (dev, inode) earlier in the same snapshot, store a LinkTo reference
		// instead of re-chunking and re-storing the content.
		if hasLink {
			a.linkMu.Lock()
			firstPath, seen := a.linkSeen[linkKey]
			if !seen {
				a.linkSeen[linkKey] = relPath
			}
			a.linkMu.Unlock()
			if seen {
				atomic.AddInt64(&stats.FilesNew, 1)
				n := &repo.Node{
					Name:    name,
					Type:    repo.NodeFile,
					Mode:    uint32(fi.Mode()),
					ModTime: fi.ModTime().UTC(),
					Size:    fi.Size(),
					LinkTo:  firstPath,
					Inode:   linkKey.Inode,
					Dev:     linkKey.Dev,
				}
				applyCollectedMeta(n, owner, hasOwner, xattrs)
				return n, nil
			}
		}

		content, err := a.archiveFile(ctx, path, stats)
		if err != nil {
			return nil, err
		}
		atomic.AddInt64(&stats.FilesNew, 1)
		atomic.AddInt64(&stats.BytesTotal, fi.Size())
		n := &repo.Node{
			Name:    name,
			Type:    repo.NodeFile,
			Mode:    uint32(fi.Mode()),
			ModTime: fi.ModTime().UTC(),
			Size:    fi.Size(),
			Content: content,
		}
		if hasLink {
			n.Inode = linkKey.Inode
			n.Dev = linkKey.Dev
		}
		applyCollectedMeta(n, owner, hasOwner, xattrs)
		return n, nil

	default:
		// Skip devices, sockets, FIFOs, etc.
		// TODO(subsequent-agents): handle special files if desired.
		return nil, nil
	}
}

// applyCollectedMeta copies OS metadata gathered by bfs.Collect onto n.
func applyCollectedMeta(n *repo.Node, owner bfs.OwnerInfo, hasOwner bool, xattrs map[string][]byte) {
	if hasOwner {
		n.UID = owner.UID
		n.GID = owner.GID
		n.OwnerSet = true
	}
	if len(xattrs) > 0 {
		n.Xattrs = xattrs
	}
}

// archiveDir reads a directory and archives each child in parallel, returning
// the child tree blob id. relDir is the snapshot-relative, slash-separated
// path of dir.
func (a *Archiver) archiveDir(ctx context.Context, dir, relDir string, opts Options, stats *Stats) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	nodes := make([]repo.Node, len(entries))
	present := make([]bool, len(entries))

	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = 4
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	var mu sync.Mutex // protects nodes/present writes (index writes are internally locked)

	for i, e := range entries {
		i, e := i, e
		g.Go(func() error {
			node, err := a.archivePath(gctx, filepath.Join(dir, e.Name()), relDir+"/"+e.Name(), opts, stats)
			if err != nil {
				return err
			}
			if node != nil {
				mu.Lock()
				nodes[i] = *node
				present[i] = true
				mu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", err
	}

	final := make([]repo.Node, 0, len(nodes))
	for i, ok := range present {
		if ok {
			final = append(final, nodes[i])
		}
	}
	sort.Slice(final, func(i, j int) bool { return final[i].Name < final[j].Name })
	return a.repo.SaveTree(gctx, final)
}

// archiveFile chunks a regular file and stores each chunk, returning the
// ordered list of content blob ids.
func (a *Archiver) archiveFile(ctx context.Context, path string, stats *Stats) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ck, err := chunker.New(f, a.chunkKey)
	if err != nil {
		return nil, err
	}

	var ids []string
	err = ck.Split(func(chunk []byte) error {
		// The chunk slice aliases the chunker buffer; SaveBlob hashes and copies
		// (via compression) synchronously, so we do not need to copy here.
		id, isNew, err := a.repo.SaveBlob(ctx, repo.BlobData, chunk)
		if err != nil {
			return err
		}
		ids = append(ids, id)
		if isNew {
			atomic.AddInt64(&stats.ChunksNew, 1)
			atomic.AddInt64(&stats.BytesNew, int64(len(chunk)))
		} else {
			atomic.AddInt64(&stats.ChunksReused, 1)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// isExcluded reports whether path matches any exclude glob (checked against the
// base name and the full path).
func isExcluded(path string, excludes []string) bool {
	if len(excludes) == 0 {
		return false
	}
	base := filepath.Base(path)
	for _, pat := range excludes {
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, path); ok {
			return true
		}
	}
	return false
}
