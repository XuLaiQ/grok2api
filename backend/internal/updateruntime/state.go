package updateruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Job struct {
	ID            string     `json:"id"`
	State         string     `json:"state"`
	TargetVersion string     `json:"targetVersion"`
	Message       string     `json:"message"`
	Error         string     `json:"error"`
	StartedAt     *time.Time `json:"startedAt"`
	UpdatedAt     *time.Time `json:"updatedAt"`
}

type Activation struct {
	JobID     string `json:"jobId"`
	Version   string `json:"version"`
	Directory string `json:"directory"`
}

func IsActive(state string) bool {
	switch state {
	case "downloading", "verifying", "extracting", "restarting":
		return true
	default:
		return false
	}
}

func ReadJob(root string) (Job, error) {
	var job Job
	err := readJSON(filepath.Join(root, "status.json"), &job)
	if errors.Is(err, os.ErrNotExist) {
		return Job{State: "idle"}, nil
	}
	return job, err
}

func WriteJob(root string, job Job) error {
	now := time.Now().UTC()
	job.UpdatedAt = &now
	if job.StartedAt == nil && IsActive(job.State) {
		job.StartedAt = &now
	}
	return WriteJSON(filepath.Join(root, "status.json"), job)
}

func RequestActivation(root string, activation Activation) error {
	if _, err := validateRelease(root, activation); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(root, "request.json")); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return errors.New("an activation is already pending")
	}
	published, err := writeJSON(filepath.Join(root, "request.json"), activation)
	// Once visible, the supervisor may already be consuming the release. Treat publication
	// as accepted even if directory fsync fails, so callers never delete an active candidate.
	if published {
		return nil
	}
	return err
}

// WriteJSON replaces a complete file atomically so polling readers never see partial JSON.
func WriteJSON(path string, value any) error {
	_, err := writeJSON(path, value)
	return err
}

func writeJSON(path string, value any) (bool, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	file, err := os.CreateTemp(dir, ".update-*.tmp")
	if err != nil {
		return false, err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(name, path); err != nil {
		return false, err
	}
	if runtime.GOOS == "linux" {
		parent, err := os.Open(dir)
		if err != nil {
			return true, err
		}
		defer parent.Close()
		return true, parent.Sync()
	}
	return true, nil
}

func readJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return fmt.Errorf("invalid update state file: %s", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("update state contains trailing JSON")
	}
	return nil
}

var directoryPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func validateRelease(root string, activation Activation) (string, error) {
	if !directoryPattern.MatchString(activation.Directory) || activation.JobID == "" || activation.Version == "" {
		return "", errors.New("invalid release activation")
	}
	directory := filepath.Join(root, "releases", activation.Directory)
	paths := []struct {
		path string
		dir  bool
	}{
		{filepath.Join(root, "releases"), true},
		{directory, true},
		{filepath.Join(directory, "grok2api"), false},
		{filepath.Join(directory, "VERSION"), false},
		{filepath.Join(directory, "frontend"), true},
		{filepath.Join(directory, "frontend", "dist"), true},
		{filepath.Join(directory, "frontend", "dist", "index.html"), false},
	}
	for _, entry := range paths {
		info, err := os.Lstat(entry.path)
		if err != nil {
			return "", fmt.Errorf("incomplete release: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != entry.dir || (!entry.dir && !info.Mode().IsRegular()) {
			return "", fmt.Errorf("unsafe release path: %s", entry.path)
		}
		if entry.path == filepath.Join(directory, "grok2api") && runtime.GOOS == "linux" && info.Mode().Perm()&0o111 == 0 {
			return "", errors.New("release binary is not executable")
		}
		if entry.path == filepath.Join(directory, "VERSION") && info.Size() > 256 {
			return "", errors.New("release VERSION is too large")
		}
	}
	version, err := os.ReadFile(filepath.Join(directory, "VERSION"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(version)) != activation.Version {
		return "", errors.New("release VERSION does not match activation")
	}
	return directory, nil
}
