package cli

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/crypto"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/restorer"
)

func newVerifyRestoreCmd() *cobra.Command {
	var samplePct float64

	cmd := &cobra.Command{
		Use:   "verify-restore <snapshot-id>",
		Short: "Restore a random sample of files and verify their content",
		Long: "Restore a random sample of a snapshot's files into a temporary directory,\n" +
			"verify each restored file's content blobs against their BLAKE3 ids, then\n" +
			"delete the temporary copy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if samplePct <= 0 || samplePct > 100 {
				return fmt.Errorf("verify-restore: --sample must be in (0,100]")
			}
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			snap, err := r.FindSnapshot(ctx, args[0])
			if err != nil {
				return err
			}

			// Collect all regular files (with content) in the snapshot.
			type fileEntry struct {
				path string
				node repo.Node
			}
			var files []fileEntry
			rs := restorer.New(r)
			if err := rs.Walk(ctx, snap, func(path string, n repo.Node) error {
				if n.Type == repo.NodeFile {
					files = append(files, fileEntry{path, n})
				}
				return nil
			}); err != nil {
				return err
			}

			// Sample: samplePct% of files, at least min(10, total).
			n := int(float64(len(files)) * samplePct / 100.0)
			minSample := 10
			if minSample > len(files) {
				minSample = len(files)
			}
			if n < minSample {
				n = minSample
			}
			if n > len(files) {
				n = len(files)
			}

			// Random selection without replacement.
			perm := rand.Perm(len(files))
			sample := make([]fileEntry, 0, n)
			for i := 0; i < n; i++ {
				sample = append(sample, files[perm[i]])
			}

			tmp, err := os.MkdirTemp("", "bakku-verify-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)

			var problems []string
			verified := 0
			bytesRead := int64(0)
			for _, fe := range sample {
				b, err := verifyAndRestoreFile(cmd, r, tmp, fe.path, fe.node)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: %v", fe.path, err))
					continue
				}
				verified++
				bytesRead += b
			}

			ok := len(problems) == 0
			if gf.json {
				out := struct {
					OK         bool     `json:"ok"`
					Snapshot   string   `json:"snapshot"`
					TotalFiles int      `json:"total_files"`
					Sampled    int      `json:"sampled"`
					Verified   int      `json:"verified"`
					Bytes      int64    `json:"bytes_verified"`
					Problems   []string `json:"problems,omitempty"`
				}{
					OK: ok, Snapshot: snap.ID, TotalFiles: len(files),
					Sampled: len(sample), Verified: verified, Bytes: bytesRead,
					Problems: problems,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(out); err != nil {
					return err
				}
			} else {
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "snapshot:    %s\n", short8(snap.ID))
				fmt.Fprintf(w, "total files: %d\n", len(files))
				fmt.Fprintf(w, "sampled:     %d (%.0f%%, min 10)\n", len(sample), samplePct)
				fmt.Fprintf(w, "verified:    %d\n", verified)
				fmt.Fprintf(w, "bytes read:  %s\n", humanBytes(bytesRead))
				if ok {
					fmt.Fprintln(w, "\nall sampled files verified successfully")
				} else {
					fmt.Fprintf(w, "\n%d problem(s):\n", len(problems))
					for _, p := range problems {
						fmt.Fprintf(w, "  - %s\n", p)
					}
				}
			}

			if !ok {
				return fmt.Errorf("verify-restore found problems")
			}
			return nil
		},
	}
	cmd.Flags().Float64Var(&samplePct, "sample", 10, "percentage of files to sample (default 10, minimum 10 files)")
	return cmd
}

// verifyAndRestoreFile writes the file's content into tmp, hashing each content
// blob to confirm it matches its recorded id, and returns the bytes verified.
func verifyAndRestoreFile(cmd *cobra.Command, r *repo.Repository, tmp, relPath string, n repo.Node) (int64, error) {
	ctx := cmd.Context()
	dst := filepath.Join(tmp, relPath)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var total int64
	for _, id := range n.Content {
		blob, err := r.LoadBlob(ctx, id)
		if err != nil {
			return total, fmt.Errorf("load blob %s: %w", short8(id), err)
		}
		h := crypto.HashChunk(blob)
		if got := hex.EncodeToString(h[:]); got != id {
			return total, fmt.Errorf("content blob %s hash mismatch (got %s)", short8(id), short8(got))
		}
		if _, err := f.Write(blob); err != nil {
			return total, err
		}
		total += int64(len(blob))
	}
	return total, nil
}
