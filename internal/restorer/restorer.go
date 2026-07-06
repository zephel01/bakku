// Package restorer reconstructs files and directories from a snapshot's tree
// into a target directory, restoring file mode and mtime.
package restorer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bfs "github.com/zephel01/bakku/internal/fs"
	"github.com/zephel01/bakku/internal/repo"
)

// Options configure a restore.
type Options struct {
	Target   string   // destination directory (created if needed)
	Includes []string // glob patterns; if non-empty, only matching paths restored

	// Chown restores uid/gid ownership when running with sufficient privilege
	// (root/Administrator); silently skipped otherwise. Default false.
	Chown bool
	// RestoreQuarantine includes the macOS com.apple.quarantine xattr (excluded
	// by default so restored files are not Gatekeeper-blocked).
	RestoreQuarantine bool
}

// pendingLink records a hard-link node whose target could not yet be resolved
// during the main tree walk (its target may live in a directory ordered after
// this one); resolved in a second pass once the whole tree has been written.
type pendingLink struct {
	dst    string // path to create
	target string // snapshot-relative path (slash-separated) of the link target
	node   repo.Node
}

// Restorer restores snapshots from a repository.
type Restorer struct {
	repo *repo.Repository

	// target is the cleaned restore root. Every destination path is confined
	// to this root (see fs.SafeJoin) so a crafted/corrupted snapshot cannot
	// write outside it via ".." or absolute entry names (path traversal).
	target string

	// linkTargets maps a snapshot-relative path (slash-separated) to the
	// restored filesystem path, populated as regular files are written so
	// hard-link nodes (LinkTo) can resolve their target.
	linkTargets  map[string]string
	pendingLinks []pendingLink

	// warnings accumulates non-fatal metadata-restore issues (failed chown,
	// failed setxattr, etc.) from the most recent Restore call.
	warnings []string
}

// Warnings returns non-fatal warnings collected during the most recent
// Restore call (e.g. failed ownership/xattr restoration). Callers may print
// these after a successful restore; they never cause Restore to fail.
func (rs *Restorer) Warnings() []string { return rs.warnings }

// New returns a Restorer bound to r.
func New(r *repo.Repository) *Restorer { return &Restorer{repo: r} }

// Restore reconstructs snap's tree under opts.Target.
func (rs *Restorer) Restore(ctx context.Context, snap *repo.Snapshot, opts Options) error {
	if opts.Target == "" {
		return fmt.Errorf("restorer: empty target")
	}
	if err := os.MkdirAll(opts.Target, 0o755); err != nil {
		return err
	}
	rs.target = filepath.Clean(opts.Target)
	nodes, err := rs.repo.LoadTree(ctx, snap.Tree)
	if err != nil {
		return err
	}
	rs.linkTargets = make(map[string]string)
	rs.pendingLinks = nil
	rs.warnings = nil

	if err := rs.restoreNodes(ctx, nodes, opts.Target, "", opts); err != nil {
		return err
	}
	return rs.resolvePendingLinks(opts)
}

// resolvePendingLinks creates hard links deferred during the main walk. It
// loops until no progress is made, so links may resolve in any discovery
// order (including targets that are themselves hard-link nodes chained
// transitively, though the archiver never produces chains today).
func (rs *Restorer) resolvePendingLinks(opts Options) error {
	remaining := rs.pendingLinks
	for len(remaining) > 0 {
		var next []pendingLink
		progress := false
		for _, pl := range remaining {
			target, ok := rs.linkTargets[pl.target]
			if !ok {
				next = append(next, pl)
				continue
			}
			_ = os.Remove(pl.dst)
			if err := bfs.Link(target, pl.dst); err != nil {
				return fmt.Errorf("restorer: hard link %s -> %s: %w", pl.dst, target, err)
			}
			rs.warnings = append(rs.warnings, applyMeta(pl.dst, pl.node, opts)...)
			progress = true
		}
		if !progress {
			// Target never materialized (e.g. excluded by includes, or failed to
			// restore). Leave these links unresolved but do not fail the whole
			// restore; report as an error listing the missing targets.
			missing := make([]string, 0, len(next))
			for _, pl := range next {
				missing = append(missing, pl.target)
			}
			return fmt.Errorf("restorer: %d hard link(s) could not resolve target(s): %v", len(missing), missing)
		}
		remaining = next
	}
	return nil
}

// restoreNodes writes each node under dir. relPath is the snapshot-relative path
// prefix used for include matching.
func (rs *Restorer) restoreNodes(ctx context.Context, nodes []repo.Node, dir, relPath string, opts Options) error {
	for _, n := range nodes {
		dst, err := bfs.SafeJoin(rs.target, dir, n.Name)
		if err != nil {
			return fmt.Errorf("restorer: %w", err)
		}
		nodeRel := filepath.Join(relPath, n.Name)
		nodeRelSlash := filepath.ToSlash(nodeRel)
		switch n.Type {
		case repo.NodeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
			child, err := rs.repo.LoadTree(ctx, n.Subtree)
			if err != nil {
				return err
			}
			if err := rs.restoreNodes(ctx, child, dst, nodeRel, opts); err != nil {
				return err
			}
			// Apply directory metadata after children are written.
			rs.warnings = append(rs.warnings, applyMeta(dst, n, opts)...)

		case repo.NodeSymlink:
			if !included(nodeRel, opts.Includes) {
				continue
			}
			_ = os.Remove(dst)
			if err := os.Symlink(n.LinkTarget, dst); err != nil {
				return err
			}
			// Note: chmod/chtimes on symlinks is platform-dependent; skipped.
			// Ownership restore for symlinks would need Lchown, deliberately
			// skipped here since symlink ownership is rarely security-relevant.

		case repo.NodeFile:
			if !included(nodeRel, opts.Includes) {
				continue
			}
			if n.LinkTo != "" {
				// Defer: the target may not be restored yet (directories are
				// walked in tree order, not archive-discovery order).
				rs.pendingLinks = append(rs.pendingLinks, pendingLink{
					dst:    dst,
					target: filepath.ToSlash(n.LinkTo),
					node:   n,
				})
				continue
			}
			if err := rs.restoreFile(ctx, dst, n, opts); err != nil {
				return err
			}
			rs.linkTargets[nodeRelSlash] = dst

		default:
			return fmt.Errorf("restorer: unknown node type %q for %s", n.Type, dst)
		}
	}
	return nil
}

// restoreFile writes a file's content chunks in order and applies metadata.
func (rs *Restorer) restoreFile(ctx context.Context, dst string, n repo.Node, opts Options) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, id := range n.Content {
		blob, err := rs.repo.LoadBlob(ctx, id)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(blob); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	rs.warnings = append(rs.warnings, applyMeta(dst, n, opts)...)
	return nil
}

// applyMeta restores file mode, mtime, ownership and extended attributes
// (best-effort; errors from OS-metadata restore are collected as warnings by
// the caller via bfs.ApplyOwnerAndXattrs, never fatal, so a permission quirk
// on one entry does not abort the whole restore).
func applyMeta(dst string, n repo.Node, opts Options) []string {
	mode := os.FileMode(n.Mode)
	_ = os.Chmod(dst, mode.Perm())
	if !n.ModTime.IsZero() {
		_ = os.Chtimes(dst, time.Now(), n.ModTime)
	}
	return bfs.ApplyOwnerAndXattrs(dst, n, bfs.RestoreOptions{
		Chown:             opts.Chown,
		RestoreQuarantine: opts.RestoreQuarantine,
	})
}

// included reports whether relPath should be restored given the include globs.
// With no includes, everything is included.
func included(relPath string, includes []string) bool {
	if len(includes) == 0 {
		return true
	}
	base := filepath.Base(relPath)
	for _, pat := range includes {
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, relPath); ok {
			return true
		}
	}
	return false
}

// Walk lists the entries of a snapshot (for `bakku ls`) invoking fn for each
// node with its snapshot-relative path.
func (rs *Restorer) Walk(ctx context.Context, snap *repo.Snapshot, fn func(path string, n repo.Node) error) error {
	nodes, err := rs.repo.LoadTree(ctx, snap.Tree)
	if err != nil {
		return err
	}
	return rs.walk(ctx, nodes, "", fn)
}

func (rs *Restorer) walk(ctx context.Context, nodes []repo.Node, prefix string, fn func(string, repo.Node) error) error {
	for _, n := range nodes {
		p := filepath.Join(prefix, n.Name)
		if err := fn(p, n); err != nil {
			return err
		}
		if n.Type == repo.NodeDir {
			child, err := rs.repo.LoadTree(ctx, n.Subtree)
			if err != nil {
				return err
			}
			if err := rs.walk(ctx, child, p, fn); err != nil {
				return err
			}
		}
	}
	return nil
}
