package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/restorer"
)

func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff <snapshot-id-1> <snapshot-id-2>",
		Short: "Show the differences between two snapshots",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			snap1, err := r.FindSnapshot(ctx, args[0])
			if err != nil {
				return err
			}
			snap2, err := r.FindSnapshot(ctx, args[1])
			if err != nil {
				return err
			}

			m1, err := collectNodes(ctx, r, snap1)
			if err != nil {
				return err
			}
			m2, err := collectNodes(ctx, r, snap2)
			if err != nil {
				return err
			}

			var added, removed, changed []string
			for p, n2 := range m2 {
				n1, ok := m1[p]
				if !ok {
					added = append(added, p)
				} else if nodeChanged(n1, n2) {
					changed = append(changed, p)
				}
			}
			for p := range m1 {
				if _, ok := m2[p]; !ok {
					removed = append(removed, p)
				}
			}
			sort.Strings(added)
			sort.Strings(removed)
			sort.Strings(changed)

			if gf.json {
				out := struct {
					From    string   `json:"from"`
					To      string   `json:"to"`
					Added   []string `json:"added"`
					Removed []string `json:"removed"`
					Changed []string `json:"changed"`
				}{
					From: snap1.ID, To: snap2.ID,
					Added: added, Removed: removed, Changed: changed,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "comparing %s -> %s\n\n", short8(snap1.ID), short8(snap2.ID))
			for _, p := range added {
				fmt.Fprintf(w, "+ %s\n", p)
			}
			for _, p := range removed {
				fmt.Fprintf(w, "- %s\n", p)
			}
			for _, p := range changed {
				fmt.Fprintf(w, "M %s\n", p)
			}
			fmt.Fprintf(w, "\n%d added, %d removed, %d changed\n", len(added), len(removed), len(changed))
			return nil
		},
	}
	return cmd
}

// collectNodes walks a snapshot and returns a map of path -> node.
func collectNodes(ctx context.Context, r *repo.Repository, snap *repo.Snapshot) (map[string]repo.Node, error) {
	m := make(map[string]repo.Node)
	rs := restorer.New(r)
	err := rs.Walk(ctx, snap, func(path string, n repo.Node) error {
		m[path] = n
		return nil
	})
	return m, err
}

// nodeChanged reports whether two nodes at the same path differ meaningfully.
func nodeChanged(a, b repo.Node) bool {
	if a.Type != b.Type {
		return true
	}
	switch a.Type {
	case repo.NodeFile:
		if len(a.Content) != len(b.Content) {
			return true
		}
		for i := range a.Content {
			if a.Content[i] != b.Content[i] {
				return true
			}
		}
		return a.Size != b.Size
	case repo.NodeSymlink:
		return a.LinkTarget != b.LinkTarget
	case repo.NodeDir:
		// Directory content changes surface via child paths, not the dir node.
		return false
	}
	return false
}
