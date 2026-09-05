package updateruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorHelper(t *testing.T) {
	if os.Getenv("GROK2API_RUNTIME_TEST_HELPER") != "1" {
		return
	}
	version := os.Getenv("GROK2API_VERSION")
	if version == "v3.0.0-bad" {
		os.Exit(42)
	}
	if version == "" {
		version = "bootstrap"
	}
	if frontend := os.Getenv("GROK2API_FRONTEND_PATH"); version != "bootstrap" && (!filepath.IsAbs(frontend) || !strings.Contains(frontend, "frontend/dist")) {
		os.Exit(43)
	}
	if os.Getenv("GROK2API_UPDATE_MANAGED") != "1" {
		os.Exit(44)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: os.Getenv("GROK2API_RUNTIME_TEST_LISTEN"), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Grok2api-Instance", os.Getenv("GROK2API_UPDATE_HEALTH_TOKEN"))
		w.Header().Set("X-Grok2api-Startup-Ready", "true")
		_, _ = io.WriteString(w, version)
	})}
	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			os.Exit(45)
		}
	}()
	<-ctx.Done()
	_ = server.Close()
	_ = os.WriteFile(filepath.Join(os.Getenv("GROK2API_UPDATE_DIR"), "stopped-"+version), []byte("terminated"), 0o600)
	os.Exit(0)
}

func TestSupervisorActivationRestartAndRollback(t *testing.T) {
	options, binary := helperOptions(t)
	start := func() func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, options) }()
		stopped := false
		stop := func() {
			t.Helper()
			if stopped {
				return
			}
			stopped = true
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("supervisor stopped with error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("supervisor did not stop its child")
			}
		}
		t.Cleanup(stop)
		return stop
	}
	stop := start()
	waitVersion(t, options.Listen, "bootstrap")
	good := fixtureRelease(t, options.Root, "good-release", "v2.0.0", binary)
	queueActivation(t, options.Root, good)
	waitJob(t, options.Root, good.JobID, "succeeded")
	waitVersion(t, options.Listen, good.Version)
	assertCurrent(t, options.Root, good)
	stop()
	if _, err := os.Stat(filepath.Join(options.Root, "stopped-"+good.Version)); err != nil {
		t.Fatalf("child did not receive graceful SIGTERM: %v", err)
	}
	stop = start()
	waitVersion(t, options.Listen, good.Version)
	bad := fixtureRelease(t, options.Root, "bad-release", "v3.0.0-bad", binary)
	queueActivation(t, options.Root, bad)
	waitJob(t, options.Root, bad.JobID, "rolled_back")
	waitVersion(t, options.Listen, good.Version)
	assertCurrent(t, options.Root, good)
	if _, err := os.Stat(filepath.Join(options.Root, "releases", bad.Directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed candidate was not removed after rollback: %v", err)
	}
	stop()
	// A previously selected release may also fail after a later container restart.
	bad = fixtureRelease(t, options.Root, "bad-release", "v3.0.0-bad", binary)
	if err := WriteJSON(filepath.Join(options.Root, "current.json"), bad); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(filepath.Join(options.Root, "previous.json"), good); err != nil {
		t.Fatal(err)
	}
	if err := WriteJob(options.Root, Job{ID: bad.JobID, State: "restarting", TargetVersion: bad.Version}); err != nil {
		t.Fatal(err)
	}
	stop = start()
	waitVersion(t, options.Listen, good.Version)
	waitJob(t, options.Root, bad.JobID, "rolled_back")
	assertCurrent(t, options.Root, good)
	stop()
}

func TestSupervisorClearsInterruptedDownload(t *testing.T) {
	options, _ := helperOptions(t)
	staging := filepath.Join(options.Root, ".staging-interrupted")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteJob(options.Root, Job{ID: "interrupted", State: "downloading", TargetVersion: "v2.0.0"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("supervisor did not stop")
		}
	})
	waitVersion(t, options.Listen, "bootstrap")
	waitJob(t, options.Root, "interrupted", "failed")
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan staging directory remains: %v", err)
	}
}

func TestHealthCheckRejectsAnotherInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Grok2api-Instance", "other-instance")
		w.Header().Set("X-Grok2api-Startup-Ready", "true")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s := supervisor{options: Options{HealthTimeout: 80 * time.Millisecond, pollInterval: 10 * time.Millisecond}, health: server.URL, client: server.Client()}
	child := &childProcess{token: "expected-instance", done: make(chan struct{})}
	if err := s.waitHealthy(context.Background(), child); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("health from another instance accepted: %v", err)
	}
}

func TestHealthCheckWaitsForStartupReadiness(t *testing.T) {
	var ready atomic.Bool
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("X-Grok2api-Instance", "expected-instance")
		w.Header().Set("X-Grok2api-Startup-Ready", fmt.Sprint(ready.Load()))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s := supervisor{options: Options{HealthTimeout: time.Second, pollInterval: 10 * time.Millisecond}, health: server.URL, client: server.Client()}
	child := &childProcess{token: "expected-instance", done: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- s.waitHealthy(context.Background(), child) }()
	deadline := time.Now().Add(time.Second)
	for requests.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if requests.Load() < 3 {
		t.Fatal("health check did not poll readiness")
	}
	select {
	case err := <-done:
		t.Fatalf("incomplete startup passed health check: %v", err)
	default:
	}
	ready.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ready instance was not accepted")
	}
}

func TestHealthCheckRejectsMissingStartupReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Grok2api-Instance", "expected-instance")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	s := supervisor{options: Options{HealthTimeout: 80 * time.Millisecond, pollInterval: 10 * time.Millisecond}, health: server.URL, client: server.Client()}
	child := &childProcess{token: "expected-instance", done: make(chan struct{})}
	if err := s.waitHealthy(context.Background(), child); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("instance without startup readiness accepted: %v", err)
	}
}

func helperOptions(t *testing.T) (Options, []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := listener.Addr().String()
	_ = listener.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK2API_RUNTIME_TEST_HELPER", "1")
	t.Setenv("GROK2API_RUNTIME_TEST_LISTEN", listen)
	t.Setenv("GROK2API_VERSION", "")
	t.Setenv("GROK2API_FRONTEND_PATH", "")
	root := t.TempDir()
	return Options{Root: root, Executable: executable, ConfigPath: filepath.Join(root, "config.yaml"), Listen: listen,
		HealthTimeout: 3 * time.Second, StopTimeout: time.Second, pollInterval: 20 * time.Millisecond,
		childArgs: []string{"-test.run=^TestSupervisorHelper$"}}, binary
}

func waitVersion(t *testing.T, listen, version string) {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + listen + "/healthz")
		if err == nil {
			data, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if string(data) == version {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("backend never served version %q", version)
}

func queueActivation(t *testing.T, root string, activation Activation) {
	t.Helper()
	if err := WriteJob(root, Job{ID: activation.JobID, State: "restarting", TargetVersion: activation.Version}); err != nil {
		t.Fatal(err)
	}
	if err := RequestActivation(root, activation); err != nil {
		t.Fatal(err)
	}
}

func waitJob(t *testing.T, root, id, state string) {
	t.Helper()
	var last Job
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		job, err := ReadJob(root)
		if err == nil {
			last = job
			if job.ID == id && job.State == state {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("job never reached %s; last status: %#v", state, last))
}

func assertCurrent(t *testing.T, root string, want Activation) {
	t.Helper()
	var actual Activation
	if err := readJSON(filepath.Join(root, "current.json"), &actual); err != nil {
		t.Fatal(err)
	}
	if actual != want {
		t.Fatalf("current release = %#v; want %#v", actual, want)
	}
}
