package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/pruner"
)

func newCheckCmd() *cobra.Command {
	var readData bool

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check the repository for structural integrity",
		Long: "Verify that every indexed blob resides in an existing pack, that every\n" +
			"blob referenced by a snapshot tree is present in the index, and that all\n" +
			"pack files exist. With --read-data, also decrypt every blob and verify its\n" +
			"BLAKE3 hash.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			var problems []string

			// 1. Enumerate packs and their sizes.
			packs, err := r.ListPacks(ctx)
			if err != nil {
				return err
			}
			packSet := make(map[string]struct{}, len(packs))
			for _, p := range packs {
				packSet[p.ID] = struct{}{}
			}

			// 2. Every indexed blob must reference an existing pack.
			index := r.IndexEntries()
			for id, loc := range index {
				if _, ok := packSet[loc.PackID]; !ok {
					problems = append(problems, fmt.Sprintf("index blob %s references missing pack %s", short8(id), short8(loc.PackID)))
				}
			}

			// 3. Every blob referenced by a snapshot tree must be in the index.
			snaps, err := r.ListSnapshots(ctx)
			if err != nil {
				return err
			}
			used, err := pruner.Reachable(ctx, r, snaps)
			if err != nil {
				problems = append(problems, fmt.Sprintf("tree walk failed: %v", err))
			}
			for id := range used {
				if _, ok := index[id]; !ok {
					problems = append(problems, fmt.Sprintf("blob %s referenced by a snapshot is missing from the index", short8(id)))
				}
			}

			// 4. Optionally read + verify every pack's data.
			blobsVerified := 0
			if readData {
				for _, p := range packs {
					if errs := r.VerifyPack(ctx, p.ID); len(errs) > 0 {
						for _, e := range errs {
							problems = append(problems, e.Error())
						}
					}
					ents, err := r.ReadPackEntries(ctx, p.ID)
					if err == nil {
						blobsVerified += len(ents)
					}
				}
			}

			ok := len(problems) == 0
			if gf.json {
				out := struct {
					OK            bool     `json:"ok"`
					Snapshots     int      `json:"snapshots"`
					Packs         int      `json:"packs"`
					IndexBlobs    int      `json:"index_blobs"`
					UsedBlobs     int      `json:"used_blobs"`
					DataVerified  bool     `json:"data_verified"`
					BlobsVerified int      `json:"blobs_verified"`
					Problems      []string `json:"problems,omitempty"`
				}{
					OK: ok, Snapshots: len(snaps), Packs: len(packs),
					IndexBlobs: len(index), UsedBlobs: len(used),
					DataVerified: readData, BlobsVerified: blobsVerified,
					Problems: problems,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(out); err != nil {
					return err
				}
			} else {
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "snapshots:   %d\n", len(snaps))
				fmt.Fprintf(w, "packs:       %d\n", len(packs))
				fmt.Fprintf(w, "index blobs: %d\n", len(index))
				fmt.Fprintf(w, "used blobs:  %d\n", len(used))
				if readData {
					fmt.Fprintf(w, "data blobs verified: %d\n", blobsVerified)
				}
				if ok {
					fmt.Fprintln(w, "\nno errors were found")
				} else {
					fmt.Fprintf(w, "\n%d problem(s) found:\n", len(problems))
					for _, p := range problems {
						fmt.Fprintf(w, "  - %s\n", p)
					}
				}
			}

			if !ok {
				return errCheckFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&readData, "read-data", false, "read and cryptographically verify every blob")
	return cmd
}

// errCheckFailed signals a non-zero exit code; the detailed problem list has
// already been printed above.
var errCheckFailed = errors.New("check found problems")
