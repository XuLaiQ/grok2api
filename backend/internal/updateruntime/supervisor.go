package updateruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Options struct {
	Root          string
	Executable    string
	ConfigPath    string
	Listen        string
	WorkDir       string
	Logger        *slog.Logger
	HealthTimeout time.Duration
	StopTimeout   time.Duration
	pollInterval  time.Duration
	childArgs     []string
}

// Run owns the backend child, keeping the bootstrap executable outside downloaded releases.
func Run(ctx context.Context, options Options) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("managed updates require Linux amd64 or arm64; use the Docker deployment")
	}
	return run(ctx, options)
}

type supervisor struct {
	options Options
	current Activation
	child   *childProcess
	client  *http.Client
	health  string
}

type childProcess struct {
	cmd   *exec.Cmd
	done  chan struct{}
	err   error
	token string
}

func run(ctx context.Context, options Options) error {
	if options.Root == "" || options.Executable == "" || options.ConfigPath == "" {
		return errors.New("update root, executable and config path are required")
	}
	var err error
	if options.Root, err = filepath.Abs(options.Root); err != nil {
		return err
	}
	if options.Executable, err = filepath.Abs(options.Executable); err != nil {
		return err
	}
	if options.ConfigPath, err = filepath.Abs(options.ConfigPath); err != nil {
		return err
	}
	if options.WorkDir == "" {
		options.WorkDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.HealthTimeout <= 0 {
		options.HealthTimeout = 90 * time.Second
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = 20 * time.Second
	}
	if options.pollInterval <= 0 {
		options.pollInterval = 500 * time.Millisecond
	}
	health, err := healthURL(options.Listen)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(options.Root, 0o700); err != nil {
		return err
	}
	unlock, err := lockSupervisor(options.Root)
	if err != nil {
		return err
	}
	defer unlock()
	if err := cleanupStaging(options.Root); err != nil {
		options.Logger.Warn("update_staging_cleanup_failed", "error", err)
	}
	s := &supervisor{
		options: options,
		health:  health,
		client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	defer s.client.CloseIdleConnections()
	defer func() { s.stop(s.child) }()
	if err := readJSON(filepath.Join(options.Root, "current.json"), &s.current); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read selected release: %w", err)
	}
	job, err := ReadJob(options.Root)
	if err != nil {
		return fmt.Errorf("read update status: %w", err)
	}
	// A request that survived a supervisor restart never activates an uncommitted release.
	if err := removeRequest(options.Root); err != nil {
		return err
	}
	interrupted := IsActive(job.State)
	if interrupted {
		job.State = "failed"
		job.Message = "Update interrupted by a service restart; the last committed release is retained"
		job.Error = "update interrupted"
	}
	s.child, err = s.startHealthy(ctx, s.current)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		if s.current.Directory == "" {
			return fmt.Errorf("start backend: %w", err)
		}
		failed := s.current
		previous, previousErr := s.readPrevious()
		if previousErr != nil {
			return previousErr
		}
		if previous.Directory == failed.Directory {
			previous = Activation{}
		}
		job = Job{ID: failed.JobID, State: "restarting", TargetVersion: failed.Version}
		if err := WriteJob(options.Root, job); err != nil {
			return err
		}
		if err := s.restore(ctx, previous, failed, job, err); err != nil {
			return err
		}
	} else if interrupted {
		if job.ID != "" && job.ID == s.current.JobID {
			job.State, job.Message, job.Error = "succeeded", "Selected release is healthy after restart", ""
		}
		if err := WriteJob(options.Root, job); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(options.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.child.done:
			return fmt.Errorf("backend exited: %v", s.child.err)
		case <-ticker.C:
			var activation Activation
			err := readJSON(filepath.Join(options.Root, "request.json"), &activation)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				if failure := s.reject(activation, err); failure != nil {
					return failure
				}
				continue
			}
			if err := s.activate(ctx, activation); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

func (s *supervisor) activate(ctx context.Context, activation Activation) error {
	if _, err := validateRelease(s.options.Root, activation); err != nil {
		return s.reject(activation, err)
	}
	job, err := ReadJob(s.options.Root)
	if err != nil {
		return err
	}
	if job.ID != activation.JobID || job.State != "restarting" {
		return s.reject(activation, errors.New("activation does not match the pending update job"))
	}
	job.Message = "Restarting the backend and checking release health"
	if err := WriteJob(s.options.Root, job); err != nil {
		return err
	}
	previous := s.current
	if err := WriteJSON(filepath.Join(s.options.Root, "previous.json"), previous); err != nil {
		return s.reject(activation, err)
	}
	s.options.Logger.Info("update_activating", "version", activation.Version)
	s.stop(s.child)
	s.child = nil
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.child, err = s.startHealthy(ctx, activation)
	if err != nil {
		return s.restore(ctx, previous, activation, job, err)
	}
	if err := WriteJSON(filepath.Join(s.options.Root, "current.json"), activation); err != nil {
		s.stop(s.child)
		s.child = nil
		return s.restore(ctx, previous, activation, job, err)
	}
	s.current = activation
	job.State, job.Message, job.Error = "succeeded", "Update installed and healthy", ""
	if err := removeRequest(s.options.Root); err != nil {
		return err
	}
	return WriteJob(s.options.Root, job)
}

func (s *supervisor) restore(ctx context.Context, previous, failed Activation, job Job, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.options.Logger.Warn("update_rolling_back", "error", cause)
	var err error
	s.child, err = s.startHealthy(ctx, previous)
	if err != nil {
		job.State, job.Message, job.Error = "failed", "Update and previous release both failed to start", fmt.Sprintf("update: %v; rollback: %v", cause, err)
		_ = WriteJob(s.options.Root, job)
		return errors.New(job.Error)
	}
	if err := WriteJSON(filepath.Join(s.options.Root, "current.json"), previous); err != nil {
		return err
	}
	s.current = previous
	job.State, job.Message, job.Error = "rolled_back", "Release failed its health check; previous code restored", cause.Error()
	if err := removeRequest(s.options.Root); err != nil {
		return err
	}
	if err := cleanupFailedRelease(s.options.Root, failed); err != nil {
		s.options.Logger.Warn("update_failed_release_cleanup_failed", "error", err)
	}
	return WriteJob(s.options.Root, job)
}

func (s *supervisor) readPrevious() (Activation, error) {
	var previous Activation
	err := readJSON(filepath.Join(s.options.Root, "previous.json"), &previous)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return previous, err
}

func (s *supervisor) reject(activation Activation, cause error) error {
	job, err := ReadJob(s.options.Root)
	if err != nil {
		return err
	}
	if job.ID == "" {
		job.ID, job.TargetVersion = activation.JobID, activation.Version
	}
	job.State, job.Message, job.Error = "failed", "Release activation rejected", cause.Error()
	if err := removeRequest(s.options.Root); err != nil {
		return err
	}
	return WriteJob(s.options.Root, job)
}

func removeRequest(root string) error {
	err := os.Remove(filepath.Join(root, "request.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *supervisor) startHealthy(ctx context.Context, activation Activation) (*childProcess, error) {
	child, err := s.start(activation)
	if err != nil {
		return nil, err
	}
	if err := s.waitHealthy(ctx, child); err != nil {
		s.stop(child)
		return nil, err
	}
	return child, nil
}

func (s *supervisor) start(activation Activation) (*childProcess, error) {
	executable := s.options.Executable
	overrides := map[string]string{
		"GROK2API_UPDATE_MANAGED": "1",
		"GROK2API_UPDATE_DIR":     s.options.Root,
	}
	if activation.Directory != "" {
		directory, err := validateRelease(s.options.Root, activation)
		if err != nil {
			return nil, err
		}
		executable = filepath.Join(directory, "grok2api")
		overrides["GROK2API_FRONTEND_PATH"] = filepath.Join(directory, "frontend", "dist")
		overrides["GROK2API_VERSION"] = activation.Version
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(nonce[:])
	overrides["GROK2API_UPDATE_HEALTH_TOKEN"] = token
	args := []string{"--config", s.options.ConfigPath, "--listen", s.options.Listen}
	if s.options.childArgs != nil {
		args = s.options.childArgs
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = s.options.WorkDir, os.Stdout, os.Stderr
	cmd.Env = overriddenEnvironment(os.Environ(), overrides)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &childProcess{cmd: cmd, done: make(chan struct{}), token: token}
	go func() {
		child.err = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

func overriddenEnvironment(existing []string, overrides map[string]string) []string {
	environment := make([]string, 0, len(existing)+len(overrides))
	for _, entry := range existing {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			environment = append(environment, entry)
		}
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func (s *supervisor) waitHealthy(ctx context.Context, child *childProcess) error {
	ctx, cancel := context.WithTimeout(ctx, s.options.HealthTimeout)
	defer cancel()
	ticker := time.NewTicker(s.options.pollInterval)
	defer ticker.Stop()
	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("backend health check: %w", ctx.Err())
		case <-child.done:
			return fmt.Errorf("backend exited before becoming healthy: %v", child.err)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.health, nil)
			if err != nil {
				return err
			}
			response, err := s.client.Do(req)
			if err != nil {
				consecutive = 0
				continue
			}
			healthy := response.StatusCode == http.StatusOK && response.Header.Get("X-Grok2api-Instance") == child.token &&
				response.Header.Get("X-Grok2api-Startup-Ready") == "true"
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if !healthy {
				consecutive = 0
				continue
			}
			consecutive++
			if consecutive >= 2 {
				select {
				case <-child.done:
					return fmt.Errorf("backend exited during health check: %v", child.err)
				default:
					return nil
				}
			}
		}
	}
}

func (s *supervisor) stop(child *childProcess) {
	if child == nil {
		return
	}
	select {
	case <-child.done:
		return
	default:
	}
	_ = child.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(s.options.StopTimeout)
	defer timer.Stop()
	select {
	case <-child.done:
	case <-timer.C:
		_ = child.cmd.Process.Kill()
		<-child.done
	}
}

func healthURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("managed updates require a fixed TCP listen address: %q", listen)
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}
