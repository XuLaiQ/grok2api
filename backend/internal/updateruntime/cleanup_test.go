package updateruntime

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCleanupFailedReleaseProtectsCommittedSelections(t *testing.T) {
	root := t.TempDir()
	current := fixtureRelease(t, root, "current", "v3.0.0", []byte("binary"))
	previous := fixtureRelease(t, root, "previous", "v2.0.0", []byte("binary"))
	failed := fixtureRelease(t, root, "failed", "v4.0.0", []byte("binary"))
	if err := WriteJSON(filepath.Join(root, "current.json"), current); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(filepath.Join(root, "previous.json"), previous); err != nil {
		t.Fatal(err)
	}
	for _, activation := range []Activation{current, previous, failed} {
		if err := cleanupFailedRelease(root, activation); err != nil {
			t.Fatal(err)
		}
	}
	for _, activation := range []Activation{current, previous} {
		if _, err := os.Stat(filepath.Join(root, "releases", activation.Directory, "grok2api")); err != nil {
			t.Fatalf("protected release removed: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "releases", failed.Directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed release remains: %v", err)
	}
	if err := cleanupFailedRelease(root, Activation{Directory: "../outside"}); err == nil {
		t.Fatal("traversal cleanup accepted")
	}
}

func TestCleanupStagingKeepsUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".staging-123", "releases", "other"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".staging-123", "archive.tar.gz"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStaging(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".staging-123")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory remains: %v", err)
	}
	for _, name := range []string{"releases", "other"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("unrelated directory removed: %v", err)
		}
	}
}

func TestCleanupDoesNotFollowSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	marker := filepath.Join(outside, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".staging-external")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink permission unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "releases"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "releases", "external")); err != nil {
		t.Fatal(err)
	}
	if err := cleanupStaging(root); err != nil {
		t.Fatal(err)
	}
	if err := cleanupFailedRelease(root, Activation{Directory: "external"}); err == nil {
		t.Fatal("symlinked failed release accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cleanup escaped its update directory: %v", err)
	}
}
