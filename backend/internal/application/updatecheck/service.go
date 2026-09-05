package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chenyme/grok2api/backend/internal/updateruntime"
)

const (
	maxReleaseBytes = 1 << 20
	maxNotesRunes   = 4096
)

var (
	ErrBusy        = errors.New("an update operation is already running")
	ErrInvalid     = errors.New("invalid update request")
	ErrUnavailable = errors.New("updater unavailable")
	repositoryPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

type Status string

const (
	StatusUnchecked       Status = "unchecked"
	StatusUpToDate        Status = "up_to_date"
	StatusUpdateAvailable Status = "update_available"
	StatusCheckFailed     Status = "check_failed"
)

type Snapshot struct {
	CurrentVersion        string     `json:"currentVersion"`
	LatestVersion         string     `json:"latestVersion"`
	UpdateAvailable       bool       `json:"updateAvailable"`
	Status                Status     `json:"status"`
	CheckedAt             *time.Time `json:"checkedAt"`
	ReleaseURL            string     `json:"releaseUrl"`
	ReleaseNotes          string     `json:"releaseNotes"`
	Error                 string     `json:"error"`
	Repository            string     `json:"repository"`
	CanInstall            bool       `json:"canInstall"`
	InstallDisabledReason string     `json:"installDisabledReason"`
}

type Options struct {
	Directory      string
	Repository     string
	Managed        bool
	DisabledReason string
}

type Service struct {
	current                string
	client                 *http.Client
	now                    func() time.Time
	options                Options
	platform, architecture string
	mu                     sync.RWMutex
	snapshot               Snapshot
	checking, installing   bool
	releaseDisabledReason  string
}

type repositoryConfig struct {
	Repository string `json:"repository"`
}

func NewService(currentVersion string, client *http.Client, options ...Options) *Service {
	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		currentVersion = "dev"
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// A caller-supplied transport is useful for tests; redirects still obey the same policy.
	safeClient := *client
	safeClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many GitHub redirects")
		}
		return validateDownloadHost(request.URL)
	}
	service := &Service{current: currentVersion, client: &safeClient, now: time.Now, platform: runtime.GOOS, architecture: runtime.GOARCH, releaseDisabledReason: "请先检查更新"}
	if len(options) > 0 {
		service.options = options[0]
	}
	repository := service.options.Repository
	var loadErr error
	if service.options.Directory != "" {
		data, err := readRepositoryConfig(filepath.Join(service.options.Directory, "config.json"))
		if err == nil {
			var config repositoryConfig
			if len(data) > 4096 {
				loadErr = errors.New("update configuration exceeds size limit")
			} else {
				loadErr = json.Unmarshal(data, &config)
			}
			repository = config.Repository
		} else if !errors.Is(err, os.ErrNotExist) {
			loadErr = err
			repository = ""
		}
	}
	repository, err := NormalizeRepository(repository)
	if err != nil {
		loadErr = err
		repository = ""
	}
	service.snapshot = Snapshot{CurrentVersion: currentVersion, Status: StatusUnchecked, Repository: repository}
	if loadErr != nil {
		service.snapshot.Status = StatusCheckFailed
		service.snapshot.Error = "读取更新仓库配置失败: " + loadErr.Error()
	}
	return service
}

func readRepositoryConfig(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("invalid update repository configuration file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, 4097))
}

func NormalizeRepository(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "https://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
			return "", fmt.Errorf("%w: 仅支持 GitHub 仓库地址", ErrInvalid)
		}
		value = strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/"), "/")
	}
	value = strings.TrimSuffix(value, ".git")
	parts := strings.Split(value, "/")
	if len(parts) != 2 || len(parts[0]) > 39 || len(parts[1]) > 100 || !repositoryPart.MatchString(parts[0]) || !repositoryPart.MatchString(parts[1]) || strings.HasSuffix(parts[0], ".") || strings.HasSuffix(parts[1], ".") {
		return "", fmt.Errorf("%w: 仓库格式应为 owner/repo 或 https://github.com/owner/repo", ErrInvalid)
	}
	return value, nil
}

func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *Service) snapshotLocked() Snapshot {
	result := cloneSnapshot(s.snapshot)
	result.InstallDisabledReason = s.disabledReasonLocked()
	result.CanInstall = result.InstallDisabledReason == ""
	return result
}

func (s *Service) disabledReasonLocked() string {
	if s.options.DisabledReason != "" {
		return s.options.DisabledReason
	}
	if !s.options.Managed {
		return "当前服务未由更新管理器启动，请使用支持网页更新的部署方式"
	}
	if s.platform != "linux" || (s.architecture != "amd64" && s.architecture != "arm64") {
		return "网页更新仅支持 Linux amd64 / arm64"
	}
	if s.options.Directory == "" {
		return "未配置持久化更新目录"
	}
	if s.snapshot.Repository == "" {
		return "请先配置 GitHub 仓库"
	}
	if _, ok := parseSemanticVersion(s.current); !ok {
		return "当前运行版本不是有效版本号，无法安全更新"
	}
	return s.releaseDisabledReason
}

func (s *Service) busyLocked() (bool, error) {
	if s.checking || s.installing {
		return true, nil
	}
	if s.options.Directory == "" {
		return false, nil
	}
	job, err := updateruntime.ReadJob(s.options.Directory)
	return updateruntime.IsActive(job.State), err
}

func (s *Service) Configure(repository string) (Snapshot, error) {
	repository, err := NormalizeRepository(repository)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	busy, err := s.busyLocked()
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if busy {
		return Snapshot{}, ErrBusy
	}
	if s.options.Directory == "" {
		return Snapshot{}, fmt.Errorf("%w: 未配置持久化更新目录", ErrUnavailable)
	}
	if err := updateruntime.WriteJSON(filepath.Join(s.options.Directory, "config.json"), repositoryConfig{Repository: repository}); err != nil {
		return Snapshot{}, fmt.Errorf("%w: 保存仓库配置失败: %v", ErrUnavailable, err)
	}
	s.snapshot = Snapshot{CurrentVersion: s.current, Status: StatusUnchecked, Repository: repository}
	s.releaseDisabledReason = "请先检查更新"
	return s.snapshotLocked(), nil
}

func (s *Service) Check(ctx context.Context) Snapshot {
	snapshot, _ := s.CheckLatest(ctx)
	return snapshot
}

func (s *Service) CheckLatest(ctx context.Context) (Snapshot, error) {
	s.mu.Lock()
	busy, err := s.busyLocked()
	if err != nil || busy {
		result := s.snapshotLocked()
		s.mu.Unlock()
		if err != nil {
			return result, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return result, ErrBusy
	}
	repository := s.snapshot.Repository
	if repository == "" {
		s.snapshot.Status = StatusCheckFailed
		s.snapshot.Error = "请先配置 GitHub 仓库"
		result := s.snapshotLocked()
		s.mu.Unlock()
		return result, nil
	}
	s.checking = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	release, fetchErr := s.fetchLatest(ctx, repository)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checking = false
	if fetchErr != nil {
		s.releaseDisabledReason = "版本检查未成功，请重新检查更新"
		s.snapshot.Status = StatusCheckFailed
		s.snapshot.UpdateAvailable = false
		s.snapshot.Error = fetchErr.Error()
		return s.snapshotLocked(), nil
	}
	checkedAt := s.now().UTC()
	current, currentOK := parseSemanticVersion(s.current)
	latest, latestOK := parseSemanticVersion(release.Tag)
	if !currentOK || !latestOK {
		s.releaseDisabledReason = "当前版本或最新版本不是有效的语义化版本"
		s.snapshot.LatestVersion, s.snapshot.ReleaseURL, s.snapshot.ReleaseNotes = release.Tag, release.URL, release.Notes
		s.snapshot.UpdateAvailable = false
		s.snapshot.Status = StatusCheckFailed
		s.snapshot.CheckedAt = &checkedAt
		s.snapshot.Error = "当前版本或最新版本不是有效的语义化版本，无法比较"
		return s.snapshotLocked(), nil
	}
	available := compareSemanticVersion(latest, current) > 0
	s.snapshot = Snapshot{CurrentVersion: s.current, LatestVersion: release.Tag, Repository: repository,
		UpdateAvailable: available, CheckedAt: &checkedAt, ReleaseURL: release.URL, ReleaseNotes: release.Notes, Status: StatusUpToDate}
	if available {
		s.snapshot.Status = StatusUpdateAvailable
		s.releaseDisabledReason = releaseInstallReason(release, repository, s.architecture)
	} else {
		s.releaseDisabledReason = "当前已是最新版本"
	}
	return s.snapshotLocked(), nil
}

type releaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type latestRelease struct {
	Tag    string
	URL    string
	Notes  string
	Assets []releaseAsset
}

func (s *Service) fetchLatest(ctx context.Context, repository string) (latestRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/releases/latest", nil)
	if err != nil {
		return latestRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "grok2api/"+s.current)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := s.client.Do(request)
	if err != nil {
		return latestRelease{}, fmt.Errorf("检查 GitHub Release 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return latestRelease{}, fmt.Errorf("GitHub Release 检查失败（HTTP %d）", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseBytes+1))
	if err != nil {
		return latestRelease{}, fmt.Errorf("读取 GitHub Release 响应: %w", err)
	}
	if len(data) > maxReleaseBytes {
		return latestRelease{}, errors.New("GitHub Release 响应超过安全上限")
	}
	var payload struct {
		Tag        string         `json:"tag_name"`
		Body       string         `json:"body"`
		Draft      bool           `json:"draft"`
		Prerelease bool           `json:"prerelease"`
		Assets     []releaseAsset `json:"assets"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return latestRelease{}, fmt.Errorf("解析 GitHub Release 响应: %w", err)
	}
	payload.Tag = strings.TrimSpace(payload.Tag)
	if payload.Tag == "" || len(payload.Tag) > 128 {
		return latestRelease{}, errors.New("GitHub Release 未返回有效版本号")
	}
	if payload.Draft || payload.Prerelease {
		return latestRelease{}, errors.New("仅支持已正式发布的 GitHub Release")
	}
	return latestRelease{Tag: payload.Tag, URL: "https://github.com/" + repository + "/releases/tag/" + url.PathEscape(payload.Tag), Notes: truncateRunes(strings.TrimSpace(payload.Body), maxNotesRunes), Assets: payload.Assets}, nil
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func cloneSnapshot(value Snapshot) Snapshot {
	if value.CheckedAt != nil {
		checkedAt := *value.CheckedAt
		value.CheckedAt = &checkedAt
	}
	return value
}
