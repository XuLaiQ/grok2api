package updateruntime

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJobPersistenceAndAtomicReplacement(t *testing.T) {
	root := t.TempDir()
	job, err := ReadJob(root)
	if err != nil || job.State != "idle" {
		t.Fatalf("missing status: %#v, %v", job, err)
	}
	job = Job{ID: "job-1", State: "downloading", TargetVersion: "v2.0.0"}
	if err := WriteJob(root, job); err != nil {
		t.Fatal(err)
	}
	job, err = ReadJob(root)
	if err != nil || job.StartedAt == nil || job.UpdatedAt == nil {
		t.Fatalf("persisted job: %#v, %v", job, err)
	}
	started := *job.StartedAt
	job.State = "succeeded"
	if err := WriteJob(root, job); err != nil {
		t.Fatal(err)
	}
	job, err = ReadJob(root)
	if err != nil || job.State != "succeeded" || !job.StartedAt.Equal(started) {
		t.Fatalf("replacement job: %#v, %v", job, err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, ".update-*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v, %v", matches, err)
	}
}

func TestActivationRejectsUnsafeAndIncompleteReleases(t *testing.T) {
	root := t.TempDir()
	activation := fixtureRelease(t, root, "release-1", "v2.0.0", []byte("binary"))
	if err := RequestActivation(root, activation); err != nil {
		t.Fatal(err)
	}
	if err := RequestActivation(root, activation); err == nil {
		t.Fatal("duplicate activation accepted")
	}
	if err := removeRequest(root); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"", ".", "..", "../release-1", `..\release-1`, "a/b", `a\b`, "/tmp/release", "C:release", strings.Repeat("x", 129)} {
		t.Run(directory, func(t *testing.T) {
			invalid := activation
			invalid.Directory = directory
			if err := RequestActivation(root, invalid); err == nil {
				t.Fatal("unsafe directory accepted")
			}
		})
	}
	activation.Version = "v3.0.0"
	if err := RequestActivation(root, activation); err == nil {
		t.Fatal("mismatched version accepted")
	}
	activation.Version = "v2.0.0"
	if err := os.Remove(filepath.Join(root, "releases", activation.Directory, "frontend", "dist", "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := RequestActivation(root, activation); err == nil {
		t.Fatal("incomplete frontend accepted")
	}
}

func TestActivationRejectsSymlinkedFrontend(t *testing.T) {
	root := t.TempDir()
	activation := fixtureRelease(t, root, "release-1", "v2.0.0", []byte("binary"))
	dist := filepath.Join(root, "releases", activation.Directory, "frontend", "dist")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "index.html"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dist); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dist); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink permission unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := RequestActivation(root, activation); err == nil {
		t.Fatal("symlinked frontend accepted")
	}
}

func TestReadJobRejectsMalformedState(t *testing.T) {
	root := t.TempDir()
	for _, input := range []string{`{"state":"downloading"`, `{"state":"idle"}{"state":"succeeded"}`} {
		if err := os.WriteFile(filepath.Join(root, "status.json"), []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadJob(root); err == nil {
			t.Fatal("malformed status accepted")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "request.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reading status created a request")
	}
}

func TestHealthURLUsesBoundInterface(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:8000": "http://127.0.0.1:8000/healthz",
		":8000":        "http://127.0.0.1:8000/healthz",
		"[::]:9000":    "http://[::1]:9000/healthz",
		"10.0.0.2:80":  "http://10.0.0.2:80/healthz",
	} {
		actual, err := healthURL(listen)
		if err != nil || actual != want {
			t.Fatalf("healthURL(%q) = %q, %v", listen, actual, err)
		}
	}
	for _, invalid := range []string{"", "8000", "localhost:0", "unix:/tmp/app.sock"} {
		if _, err := healthURL(invalid); err == nil {
			t.Fatalf("invalid listen %q accepted", invalid)
		}
	}
}

func fixtureRelease(t *testing.T, root, directory, version string, binary []byte) Activation {
	t.Helper()
	dir := filepath.Join(root, "releases", directory)
	if err := os.MkdirAll(filepath.Join(dir, "frontend", "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"grok2api":                 binary,
		"VERSION":                  []byte(version + "\n"),
		"frontend/dist/index.html": []byte("<!doctype html><title>release</title>"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return Activation{JobID: directory, Version: version, Directory: directory}
}
