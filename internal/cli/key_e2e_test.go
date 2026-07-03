package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// decodeJSON unmarshals s into v.
func decodeJSON(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// runCLI executes the root command with the given args and environment-provided
// passwords, returning stdout and any error.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// Reset global flags between invocations (cobra persists them across a shared
	// gf var since flags bind to package-level vars).
	gf = globalFlags{}
	keyAddNewPasswordFile = ""
	keyAddYubiKey = false
	keyAddYubiKeySlot = 0
	keyRemoveForce = false

	root := NewRootCmd(BuildInfo{Version: "test"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// TestE2EKeyLifecycle drives the full flow through the CLI: init, add a second
// key, verify both open, remove one, verify the remaining opens.
func TestE2EKeyLifecycle(t *testing.T) {
	dir := t.TempDir()
	repoURL := "file://" + dir

	// init with the first password.
	t.Setenv("BAKKU_PASSWORD", "pw-one")
	if out, err := runCLI(t, "--repo", repoURL, "init"); err != nil {
		t.Fatalf("init failed: %v (%s)", err, out)
	}

	// key add with a new (second) password.
	t.Setenv("BAKKU_NEW_PASSWORD", "pw-two")
	out, err := runCLI(t, "--repo", repoURL, "key", "add")
	if err != nil {
		t.Fatalf("key add failed: %v (%s)", err, out)
	}
	if !strings.Contains(out, "added key slot") {
		t.Fatalf("unexpected key add output: %s", out)
	}

	// Both passwords must open the repo (list slots as a proxy for opening).
	for _, pw := range []string{"pw-one", "pw-two"} {
		t.Setenv("BAKKU_PASSWORD", pw)
		out, err := runCLI(t, "--repo", repoURL, "key", "list")
		if err != nil {
			t.Fatalf("key list with %q failed: %v (%s)", pw, err, out)
		}
		if strings.Count(out, "password") < 2 {
			t.Fatalf("expected 2 password slots listed, got: %s", out)
		}
	}

	// Get the id of the non-current slot when opened with pw-one, then remove it.
	t.Setenv("BAKKU_PASSWORD", "pw-one")
	out, err = runCLI(t, "--repo", repoURL, "--json", "key", "list")
	if err != nil {
		t.Fatalf("json key list failed: %v (%s)", err, out)
	}
	// Extract the id of the slot NOT marked current. Parse the JSON minimally.
	nonCurrentID := extractNonCurrentID(t, out)

	out, err = runCLI(t, "--repo", repoURL, "key", "remove", nonCurrentID)
	if err != nil {
		t.Fatalf("key remove failed: %v (%s)", err, out)
	}
	if !strings.Contains(out, "removed key slot") {
		t.Fatalf("unexpected remove output: %s", out)
	}

	// pw-two (removed slot) must no longer open; pw-one must still work.
	t.Setenv("BAKKU_PASSWORD", "pw-two")
	if _, err := runCLI(t, "--repo", repoURL, "key", "list"); err == nil {
		t.Fatal("expected pw-two to fail after its slot was removed")
	}
	t.Setenv("BAKKU_PASSWORD", "pw-one")
	if _, err := runCLI(t, "--repo", repoURL, "key", "list"); err != nil {
		t.Fatalf("pw-one should still open after removal: %v", err)
	}

	// Removing the last remaining slot must be refused.
	out, err = runCLI(t, "--repo", repoURL, "--json", "key", "list")
	if err != nil {
		t.Fatal(err)
	}
	lastID := extractAnyID(t, out)
	if _, err := runCLI(t, "--repo", repoURL, "key", "remove", lastID); err == nil {
		t.Fatal("expected removal of the last slot to be refused")
	}
}

// extractNonCurrentID pulls the "id" whose "current" is false from key list
// --json output. Kept intentionally simple (no full JSON decode dependency).
func extractNonCurrentID(t *testing.T, jsonOut string) string {
	t.Helper()
	var infos []struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
	}
	if err := decodeJSON(jsonOut, &infos); err != nil {
		t.Fatalf("decode key list json: %v (%s)", err, jsonOut)
	}
	for _, i := range infos {
		if !i.Current {
			return i.ID
		}
	}
	t.Fatalf("no non-current slot in %s", jsonOut)
	return ""
}

func extractAnyID(t *testing.T, jsonOut string) string {
	t.Helper()
	var infos []struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(jsonOut, &infos); err != nil {
		t.Fatalf("decode key list json: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("no slots")
	}
	return infos[0].ID
}
