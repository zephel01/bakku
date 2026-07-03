package repo

import (
	"encoding/json"
	"time"
)

// NodeType is the kind of filesystem entry a tree node represents.
type NodeType string

const (
	// NodeFile is a regular file; Content holds its content blob ids.
	NodeFile NodeType = "file"
	// NodeDir is a directory; Subtree holds the child tree blob id.
	NodeDir NodeType = "dir"
	// NodeSymlink is a symbolic link; LinkTarget holds its target path.
	NodeSymlink NodeType = "symlink"
)

// Node is a single entry in a directory tree. Directories reference a child
// tree blob (Subtree); files reference their ordered content chunk blob ids
// (Content). Trees are serialized as a JSON array of Nodes and stored as a
// BlobTree blob.
//
// Phase 6 adds optional OS metadata (ownership, extended attributes, hard
// link tracking, Windows owner SID). All new fields are omitempty so trees
// written before Phase 6 remain byte-compatible and old bakku binaries reading
// a new tree simply ignore the extra fields.
type Node struct {
	Name       string    `json:"name"`
	Type       NodeType  `json:"type"`
	Mode       uint32    `json:"mode"` // os.FileMode bits
	ModTime    time.Time `json:"mtime"`
	Size       int64     `json:"size"`
	Content    []string  `json:"content,omitempty"` // file: ordered content blob ids
	Subtree    string    `json:"subtree,omitempty"` // dir: child tree blob id
	LinkTarget string    `json:"link,omitempty"`    // symlink: target path

	// --- Phase 6: OS metadata ---

	// Uid/Gid are the POSIX owner/group ids (Linux/macOS). Zero value is a
	// valid uid/gid (root), so a Valid flag is needed to distinguish "unset"
	// (Windows, or platforms where we could not read ownership) from uid 0.
	UID      uint32 `json:"uid,omitempty"`
	GID      uint32 `json:"gid,omitempty"`
	OwnerSet bool   `json:"owner_set,omitempty"`

	// OwnerSID is the Windows owner security identifier string (e.g.
	// "S-1-5-21-..."), captured/restored best-effort on Windows only.
	OwnerSID string `json:"owner_sid,omitempty"`

	// Xattrs holds extended attributes keyed by name (Linux/macOS). Values are
	// raw bytes, base64-encoded implicitly by encoding/json ([]byte -> string).
	Xattrs map[string][]byte `json:"xattrs,omitempty"`

	// --- Hard link support ---

	// Inode/Dev identify the underlying filesystem inode (Linux/macOS only;
	// used transiently during archiving to detect hard links within a single
	// snapshot). Not required for correctness of restore, but kept on the node
	// for diagnostics/tooling; omitempty keeps old-format trees unaffected.
	Inode uint64 `json:"inode,omitempty"`
	Dev   uint64 `json:"dev,omitempty"`

	// LinkTo, when set on a NodeFile, means this node is a hard link to an
	// earlier file at the given snapshot-relative path (slash-separated, root
	// relative) already materialized earlier in the same snapshot. Content is
	// empty in that case; the restorer creates a hard link (os.Link) to the
	// already-restored target instead of re-writing content.
	LinkTo string `json:"link_to,omitempty"`
}

// marshalTree serializes a slice of nodes to the canonical tree blob bytes.
func marshalTree(nodes []Node) ([]byte, error) {
	return json.Marshal(nodes)
}

// unmarshalTree parses tree blob bytes back into nodes.
func unmarshalTree(b []byte) ([]Node, error) {
	var nodes []Node
	if err := json.Unmarshal(b, &nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}
