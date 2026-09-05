package updatecheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/updateruntime"
)

func TestRepositoryConfigurationAndPersistence(t *testing.T) {
	for _, input := range []string{"owner/repo", "https://github.com/owner/repo", "https://github.com/owner/repo.git"} {
		actual, err := NormalizeRepository(input)
		if err != nil || actual != "owner/repo" {
			t.Fatalf("normalize %q = %q, %v", input, actual, err)
		}
	}
	for _, input := range []string{"https://evil.example/owner/repo", "http://github.com/owner/repo", "https://token@github.com/owner/repo", "https://github.com/owner/repo?q=secret", "https://github.com/owner/repo#tag", "https://github.com:443/owner/repo", "owner/../repo", "owner/repo/more", "owner/%2e%2e", "https://github.com/owner/%72epo", "owner/.."} {
		if _, err := NormalizeRepository(input); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted repository %q: %v", input, err)
		}
	}
	directory := t.TempDir()
	service := NewService("v3.0.0", nil, Options{Directory: directory})
	if snapshot := service.Check(context.Background()); snapshot.Repository != "" || snapshot.Status != StatusCheckFailed {
		t.Fatalf("default = %#v", snapshot)
	}
	if _, err := service.Configure("owner/repo"); err != nil {
		t.Fatal(err)
	}
	reloaded := NewService("v3.0.0", nil, Options{Directory: directory, Repository: "other/repo"})
	if reloaded.Snapshot().Repository != "owner/repo" {
		t.Fatal("saved repository did not override environment default")
	}
	if _, err := reloaded.Configure(""); err != nil {
		t.Fatal(err)
	}
	if NewService("v3.0.0", nil, Options{Directory: directory, Repository: "other/repo"}).Snapshot().Repository != "" {
		t.Fatal("cleared repository was replaced by environment default")
	}
}

type archiveEntry struct {
	name string
	data []byte
	kind byte
}

func releaseArchive(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(entry.data))
		if kind != tar.TypeReg {
			size = 0
		}
		if err := archive.WriteHeader(&tar.Header{Name: entry.name, Typeflag: kind, Size: size, Mode: 0644, Linkname: "../escape"}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := archive.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func validReleaseArchive(t *testing.T, version string) []byte {
	// A minimal ELF header exercises architecture parsing without running downloaded code.
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)
	binary.LittleEndian.PutUint16(data[18:], 62)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint16(data[52:], 64)
	return releaseArchive(t, []archiveEntry{{name: "grok2api", data: data}, {name: "VERSION", data: []byte(version + "\n")}, {name: "frontend/dist/index.html", data: []byte("<!doctype html><title>Updated</title>")}})
}

func installFixture(t *testing.T, archive []byte, wrongChecksum bool) (*Service, *atomic.Int32) {
	t.Helper()
	const name = "grok2api_v3.0.1_linux_amd64.tar.gz"
	checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), name)
	if wrongChecksum {
		checksum = strings.Repeat("0", 64) + "  " + name + "\n"
	}
	assetURL := "https://github.com/owner/repo/releases/download/v3.0.1/"
	metadata, err := json.Marshal(map[string]any{"tag_name": "v3.0.1", "assets": []releaseAsset{
		{Name: name, URL: assetURL + name, Size: int64(len(archive))},
		{Name: "checksums.txt", URL: assetURL + "checksums.txt", Size: int64(len(checksum))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	requests := &atomic.Int32{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var data []byte
		switch request.URL.String() {
		case "https://api.github.com/repos/owner/repo/releases/latest":
			requests.Add(1)
			data = metadata
		case assetURL + name:
			data = archive
		case assetURL + "checksums.txt":
			data = []byte(checksum)
		default:
			return nil, fmt.Errorf("unexpected request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header), ContentLength: int64(len(data))}, nil
	})}
	service := NewService("v3.0.0", client, Options{Directory: t.TempDir(), Repository: "owner/repo", Managed: true})
	service.platform, service.architecture = "linux", "amd64"
	if snapshot := service.Check(context.Background()); !snapshot.UpdateAvailable {
		t.Fatalf("check = %#v", snapshot)
	}
	return service, requests
}

func waitForInstall(t *testing.T, service *Service) updateruntime.Job {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		service.mu.RLock()
		active := service.installing
		service.mu.RUnlock()
		if !active {
			job, err := service.Job()
			if err != nil {
				t.Fatal(err)
			}
			return job
		}
		select {
		case <-deadline.C:
			t.Fatal("install did not finish")
		case <-tick.C:
		}
	}
}

func TestInstallVerifiesStagesAndPersistsActivation(t *testing.T) {
	service, requests := installFixture(t, validReleaseArchive(t, "v3.0.1"), false)
	job, err := service.Install("v3.0.1")
	if err != nil || job.State != "downloading" {
		t.Fatalf("install = %#v, %v", job, err)
	}
	job = waitForInstall(t, service)
	if job.State != "restarting" || job.Error != "" {
		t.Fatalf("job = %#v", job)
	}
	if requests.Load() != 2 {
		t.Fatalf("latest release should be re-fetched before install; requests=%d", requests.Load())
	}
	data, err := os.ReadFile(filepath.Join(service.options.Directory, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var activation updateruntime.Activation
	if err := json.Unmarshal(data, &activation); err != nil {
		t.Fatal(err)
	}
	if activation.JobID != job.ID || activation.Version != "v3.0.1" || activation.Directory == activation.Version || strings.ContainsAny(activation.Directory, "/\\") {
		t.Fatalf("activation = %#v", activation)
	}
	if err := validateReleaseContents(filepath.Join(service.options.Directory, "releases", activation.Directory), "v3.0.1", "amd64"); err != nil {
		t.Fatal(err)
	}
	reloaded := NewService("v3.0.1", nil, Options{Directory: service.options.Directory})
	saved, err := reloaded.Job()
	if err != nil || saved.ID != job.ID || saved.State != "restarting" {
		t.Fatalf("reloaded = %#v, %v", saved, err)
	}
	if _, err := service.Configure("other/repo"); !errors.Is(err, ErrBusy) {
		t.Fatalf("configuration allowed while restart is pending: %v", err)
	}
}

func TestInstallFailuresDoNotRequestActivation(t *testing.T) {
	for _, test := range []struct {
		name     string
		archive  []byte
		checksum bool
		message  string
	}{
		{name: "checksum mismatch", archive: validReleaseArchive(t, "v3.0.1"), checksum: true, message: "SHA-256"},
		{name: "version mismatch", archive: validReleaseArchive(t, "v3.0.2"), message: "VERSION"},
		{name: "archive traversal", archive: releaseArchive(t, []archiveEntry{{name: "../outside", data: []byte("unsafe")}}), message: "不安全路径"},
		{name: "missing executable", archive: releaseArchive(t, []archiveEntry{{name: "VERSION", data: []byte("v3.0.1")}}), message: "grok2api"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _ := installFixture(t, test.archive, test.checksum)
			if _, err := service.Install("v3.0.1"); err != nil {
				t.Fatal(err)
			}
			job := waitForInstall(t, service)
			if job.State != "failed" || !strings.Contains(job.Error, test.message) {
				t.Fatalf("job = %#v", job)
			}
			if _, err := os.Stat(filepath.Join(service.options.Directory, "request.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed install published activation: %v", err)
			}
			entries, err := os.ReadDir(service.options.Directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".staging-") {
					t.Fatal("staging directory leaked")
				}
			}
		})
	}
}

func TestFailedActivationRemovesOnlyNewlyPreparedRelease(t *testing.T) {
	service, _ := installFixture(t, validReleaseArchive(t, "v3.0.1"), false)
	root := service.options.Directory
	previous := filepath.Join(root, "releases", "existing-version")
	if err := os.MkdirAll(previous, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "keep.txt"), []byte("existing release"), 0600); err != nil {
		t.Fatal(err)
	}
	pending := []byte(`{"jobId":"another-job","version":"v3.0.0","directory":"existing-version"}`)
	if err := os.WriteFile(filepath.Join(root, "request.json"), pending, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Install("v3.0.1"); err != nil {
		t.Fatal(err)
	}
	job := waitForInstall(t, service)
	if job.State != "failed" || !strings.Contains(job.Error, "activation is already pending") {
		t.Fatalf("job = %#v", job)
	}
	entries, err := os.ReadDir(filepath.Join(root, "releases"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "existing-version" {
		t.Fatalf("unexpected remaining releases: %#v", entries)
	}
	if data, err := os.ReadFile(filepath.Join(previous, "keep.txt")); err != nil || string(data) != "existing release" {
		t.Fatalf("existing release changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "request.json")); err != nil || !bytes.Equal(data, pending) {
		t.Fatalf("another activation changed: %q, %v", data, err)
	}
}

func TestPreparedReleaseCleanupRejectsUnsafeDirectory(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"", ".", "..", "../outside", "existing-version", strings.Repeat("a", 32) + "/nested"} {
		if err := removePreparedRelease(root, directory); err == nil {
			t.Errorf("accepted unsafe cleanup directory %q", directory)
		}
	}
}

func TestInstallBlocksConcurrentOperationsAndRejectsChangedLatest(t *testing.T) {
	service, _ := installFixture(t, validReleaseArchive(t, "v3.0.1"), false)
	entered, release := make(chan struct{}), make(chan struct{})
	service.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(entered)
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v3.0.2"}`))}, nil
	})
	if _, err := service.Install("v3.0.0"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale request = %v", err)
	}
	if _, err := service.Install("v3.0.1"); err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := service.Install("v3.0.1"); !errors.Is(err, ErrBusy) {
		t.Errorf("second install = %v", err)
	}
	if _, err := service.Configure("other/repo"); !errors.Is(err, ErrBusy) {
		t.Errorf("configure = %v", err)
	}
	if _, err := service.CheckLatest(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("check = %v", err)
	}
	close(release)
	job := waitForInstall(t, service)
	if job.State != "failed" || !strings.Contains(job.Error, "最新版本已变化") {
		t.Fatalf("job = %#v", job)
	}
}

func TestExtractRejectsUnsafeEntries(t *testing.T) {
	for _, entries := range [][]archiveEntry{
		{{name: "/absolute", data: []byte("bad")}},
		{{name: "folder/../../outside", data: []byte("bad")}},
		{{name: "C:\\outside", data: []byte("bad")}},
		{{name: "link", kind: tar.TypeSymlink}},
		{{name: "link", kind: tar.TypeLink}},
		{{name: "pipe", kind: tar.TypeFifo}},
		{{name: "same", data: []byte("one")}, {name: "same", data: []byte("two")}},
		{{name: "Same", data: []byte("one")}, {name: "same", data: []byte("two")}},
	} {
		directory := t.TempDir()
		filename := filepath.Join(directory, "input.tar.gz")
		if err := os.WriteFile(filename, releaseArchive(t, entries), 0600); err != nil {
			t.Fatal(err)
		}
		if err := extractArchive(context.Background(), filename, filepath.Join(directory, "contents")); err == nil {
			t.Errorf("accepted archive entries: %#v", entries)
		}
	}
}

func TestGitHubDownloadAddressPolicy(t *testing.T) {
	for _, raw := range []string{"http://github.com/file", "https://github.com.evil.test/file", "https://github.com:443/file", "https://secret@github.com/file", "https://127.0.0.1/file", "https://unrelated.githubusercontent.com/file"} {
		parsed, _ := url.Parse(raw)
		if err := validateDownloadHost(parsed); err == nil {
			t.Errorf("allowed %s", raw)
		}
	}
	for _, raw := range []string{"https://release-assets.githubusercontent.com/path?sig=abc", "https://objects.githubusercontent.com/path"} {
		parsed, _ := url.Parse(raw)
		if err := validateDownloadHost(parsed); err != nil {
			t.Errorf("rejected official redirect %s: %v", raw, err)
		}
	}
	asset := releaseAsset{Name: "checksums.txt", URL: "https://github.com/other/repo/releases/download/v3.0.1/checksums.txt", Size: 100}
	if _, err := findAsset(latestRelease{Tag: "v3.0.1", Assets: []releaseAsset{asset}}, "owner/repo", "checksums.txt", 1000); err == nil {
		t.Fatal("accepted asset from another repository")
	}
	asset.URL = "https://github.com/Owner/Repo/releases/download/v3.0.1/checksums.txt"
	if _, err := findAsset(latestRelease{Tag: "v3.0.1", Assets: []releaseAsset{asset}}, "OWNER/repo", "checksums.txt", 1000); err != nil {
		t.Fatalf("repository case should be ignored: %v", err)
	}
	asset.URL = "https://github.com/Owner/Repo/releases/download/V3.0.1/checksums.txt"
	if _, err := findAsset(latestRelease{Tag: "v3.0.1", Assets: []releaseAsset{asset}}, "owner/repo", "checksums.txt", 1000); err == nil {
		t.Fatal("release tag case must match exactly")
	}
}

func TestCheckUnusableGitHubResponses(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, `{"message":"Not Found"}`},
		{http.StatusForbidden, `{"message":"API rate limit exceeded"}`},
		{http.StatusOK, `not JSON`},
		{http.StatusOK, `{"tag_name":""}`},
		{http.StatusOK, `{"tag_name":"v3.0.1","draft":true}`},
		{http.StatusOK, `{"tag_name":"v3.0.1-rc.1","prerelease":true}`},
		{http.StatusOK, `{"tag_name":"v3.0.1-"}`},
	} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
		})}
		snapshot := NewService("v3.0.0", client, Options{Repository: "owner/repo"}).Check(context.Background())
		if snapshot.Status != StatusCheckFailed || snapshot.UpdateAvailable || snapshot.Error == "" {
			t.Errorf("response %d %s = %#v", test.status, test.body, snapshot)
		}
	}
}

func TestStrictSemanticVersionOrdering(t *testing.T) {
	for _, invalid := range []string{"3.0", "v3.0.1-", "3.0.1+", "3.0.1-rc.01", "3.0.1-rc..1", "3.00.1", "3.0.1/unsafe"} {
		if _, ok := parseSemanticVersion(invalid); ok {
			t.Errorf("accepted %q", invalid)
		}
	}
	older, _ := parseSemanticVersion("3.0.1-rc.2")
	newer, _ := parseSemanticVersion("3.0.1-rc.10")
	if compareSemanticVersion(newer, older) <= 0 {
		t.Fatal("numeric prerelease identifiers were ordered lexically")
	}
}

func TestMissingAssetsDisableInstallationAndFailedCheckClearsAvailability(t *testing.T) {
	service, _ := installFixture(t, validReleaseArchive(t, "v3.0.1"), false)
	if !service.Snapshot().CanInstall {
		t.Fatal("valid release cannot be installed")
	}
	service.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v3.0.2"}`))}, nil
	})
	snapshot := service.Check(context.Background())
	if !snapshot.UpdateAvailable || snapshot.CanInstall || !strings.Contains(snapshot.InstallDisabledReason, "grok2api_v3.0.2_linux_amd64.tar.gz") {
		t.Fatalf("missing assets = %#v", snapshot)
	}
	service.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	snapshot = service.Check(context.Background())
	if snapshot.CanInstall || snapshot.UpdateAvailable || snapshot.Status != StatusCheckFailed {
		t.Fatalf("failed check = %#v", snapshot)
	}
	if _, err := service.Install("v3.0.2"); err == nil {
		t.Fatal("failed check still permits installation")
	}
}

func TestDownloadRejectsTruncatedAndOversizedAssets(t *testing.T) {
	for _, test := range []struct {
		name, body      string
		declared, limit int64
	}{
		{"truncated", "short", 10, 20},
		{"oversized", "this exceeds the limit", 22, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body)), ContentLength: -1}, nil
			})}
			service := NewService("v3.0.0", client)
			asset := releaseAsset{URL: "https://github.com/owner/repo/releases/download/v3.0.1/checksums.txt", Size: test.declared}
			if err := service.download(context.Background(), asset, filepath.Join(t.TempDir(), "asset"), test.limit); err == nil {
				t.Fatal("invalid download succeeded")
			}
		})
	}
}

func TestCheckPreventsConcurrentRepositoryChange(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v3.0.1"}`))}, nil
	})}
	service := NewService("v3.0.0", client, Options{Directory: t.TempDir(), Repository: "owner/repo"})
	done := make(chan Snapshot, 1)
	go func() { done <- service.Check(context.Background()) }()
	<-entered
	if _, err := service.Configure("other/repo"); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent configure = %v", err)
	}
	close(release)
	if snapshot := <-done; snapshot.Repository != "owner/repo" || snapshot.LatestVersion != "v3.0.1" {
		t.Fatalf("check used inconsistent repository: %#v", snapshot)
	}
	if snapshot, err := service.Configure("other/repo"); err != nil || snapshot.Status != StatusUnchecked || snapshot.LatestVersion != "" {
		t.Fatalf("configure after check = %#v, %v", snapshot, err)
	}
}
