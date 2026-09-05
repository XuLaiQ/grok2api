package updatecheck

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chenyme/grok2api/backend/internal/updateruntime"
)

const (
	maxArchiveBytes   int64 = 256 << 20
	maxExtractedBytes int64 = 768 << 20
	maxArchiveFiles         = 20000
	installTimeout          = 10 * time.Minute
)

func (s *Service) Job() (updateruntime.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.options.Directory == "" {
		return updateruntime.Job{State: "idle"}, nil
	}
	job, err := updateruntime.ReadJob(s.options.Directory)
	if err != nil {
		return job, fmt.Errorf("%w: 读取更新状态失败: %v", ErrUnavailable, err)
	}
	return job, nil
}

func (s *Service) Install(version string) (updateruntime.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	busy, err := s.busyLocked()
	if err != nil {
		return updateruntime.Job{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if busy {
		return updateruntime.Job{}, ErrBusy
	}
	if reason := s.disabledReasonLocked(); reason != "" {
		return updateruntime.Job{}, fmt.Errorf("%w: %s", ErrUnavailable, reason)
	}
	if version == "" || version != s.snapshot.LatestVersion || s.snapshot.Status != StatusUpdateAvailable || !s.snapshot.UpdateAvailable {
		return updateruntime.Job{}, fmt.Errorf("%w: 请先检查更新并选择最新版本", ErrInvalid)
	}
	current, currentOK := parseSemanticVersion(s.current)
	target, targetOK := parseSemanticVersion(version)
	if !currentOK || !targetOK || compareSemanticVersion(target, current) <= 0 {
		return updateruntime.Job{}, fmt.Errorf("%w: 不允许安装相同或更旧的版本", ErrInvalid)
	}
	id, err := opaqueID()
	if err != nil {
		return updateruntime.Job{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	now := s.now().UTC()
	job := updateruntime.Job{ID: id, State: "downloading", TargetVersion: version, Message: "正在重新确认 GitHub Release", StartedAt: &now, UpdatedAt: &now}
	if err := updateruntime.WriteJob(s.options.Directory, job); err != nil {
		return updateruntime.Job{}, fmt.Errorf("%w: 保存更新任务失败: %v", ErrUnavailable, err)
	}
	s.installing = true
	go s.runInstall(job, s.snapshot.Repository)
	return job, nil
}

func (s *Service) runInstall(job updateruntime.Job, repository string) {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	err := s.prepareRelease(ctx, &job, repository)
	if err != nil {
		job.State, job.Message, job.Error = "failed", "更新失败，当前版本继续运行", err.Error()
		_ = updateruntime.WriteJob(s.options.Directory, job)
	}
	s.mu.Lock()
	s.installing = false
	s.mu.Unlock()
}

func (s *Service) prepareRelease(ctx context.Context, job *updateruntime.Job, repository string) (resultErr error) {
	release, err := s.fetchLatest(ctx, repository)
	if err != nil {
		return err
	}
	if release.Tag != job.TargetVersion {
		return errors.New("GitHub 最新版本已变化，请重新检查更新")
	}
	target, valid := parseSemanticVersion(release.Tag)
	current, currentValid := parseSemanticVersion(s.current)
	if !valid || !currentValid || compareSemanticVersion(target, current) <= 0 {
		return errors.New("GitHub Release 版本无效或不高于当前版本")
	}
	assetName := "grok2api_" + release.Tag + "_linux_" + s.architecture + ".tar.gz"
	archive, err := findAsset(release, repository, assetName, maxArchiveBytes)
	if err != nil {
		return err
	}
	checksums, err := findAsset(release, repository, "checksums.txt", maxReleaseBytes)
	if err != nil {
		return err
	}
	root := s.options.Directory
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(root, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	checksumPath := filepath.Join(staging, "checksums.txt")
	if err := s.download(ctx, checksums, checksumPath, maxReleaseBytes); err != nil {
		return fmt.Errorf("下载校验和: %w", err)
	}
	checksumData, err := os.ReadFile(checksumPath)
	if err != nil {
		return err
	}
	expected, err := checksumFor(checksumData, assetName)
	if err != nil {
		return err
	}
	if err := s.transition(job, "downloading", "正在下载安装包"); err != nil {
		return err
	}
	archivePath := filepath.Join(staging, "release.tar.gz")
	if err := s.download(ctx, archive, archivePath, maxArchiveBytes); err != nil {
		return fmt.Errorf("下载安装包: %w", err)
	}
	if err := s.transition(job, "verifying", "正在验证 SHA-256 校验和"); err != nil {
		return err
	}
	if err := verifyChecksum(archivePath, expected); err != nil {
		return err
	}
	if err := s.transition(job, "extracting", "正在解压并验证发布内容"); err != nil {
		return err
	}
	contents := filepath.Join(staging, "contents")
	if err := extractArchive(ctx, archivePath, contents); err != nil {
		return err
	}
	if err := validateReleaseContents(contents, release.Tag, s.architecture); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	releaseID, err := opaqueID()
	if err != nil {
		return err
	}
	if err := os.Rename(contents, filepath.Join(root, "releases", releaseID)); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			if err := removePreparedRelease(root, releaseID); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("清理未激活的发布目录失败: %w", err))
			}
		}
	}()
	if err := s.transition(job, "restarting", "安装包已就绪，等待服务重启"); err != nil {
		return err
	}
	// The supervisor owns job status once the activation request has been published.
	if err := updateruntime.RequestActivation(root, updateruntime.Activation{JobID: job.ID, Version: release.Tag, Directory: releaseID}); err != nil {
		return err
	}
	published = true
	return nil
}

func removePreparedRelease(root, releaseID string) error {
	id, err := hex.DecodeString(releaseID)
	if err != nil || len(id) != 16 || hex.EncodeToString(id) != releaseID {
		return errors.New("refusing to remove an invalid prepared release directory")
	}
	releases, err := filepath.Abs(filepath.Join(root, "releases"))
	if err != nil {
		return err
	}
	directory := filepath.Join(releases, releaseID)
	relative, err := filepath.Rel(releases, directory)
	if err != nil || relative != releaseID {
		return errors.New("prepared release directory is outside the release root")
	}
	for _, name := range []string{releases, directory} {
		info, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("refusing to remove a linked or invalid prepared release directory")
		}
	}
	return os.RemoveAll(directory)
}

func (s *Service) transition(job *updateruntime.Job, state, message string) error {
	job.State, job.Message = state, message
	now := s.now().UTC()
	job.UpdatedAt = &now
	return updateruntime.WriteJob(s.options.Directory, *job)
}

func opaqueID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validateDownloadHost(value *url.URL) error {
	if value.Scheme != "https" || value.User != nil || value.Port() != "" || value.Fragment != "" {
		return errors.New("更新资源必须使用 GitHub HTTPS 地址")
	}
	switch value.Host {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
		return nil
	default:
		return errors.New("更新资源重定向到了不受信任的域名")
	}
}

func findAsset(release latestRelease, repository, name string, limit int64) (releaseAsset, error) {
	var result releaseAsset
	count := 0
	for _, asset := range release.Assets {
		if asset.Name == name {
			result = asset
			count++
		}
	}
	if count != 1 {
		return releaseAsset{}, fmt.Errorf("Release 必须包含唯一的 %s", name)
	}
	if result.Size <= 0 || result.Size > limit {
		return releaseAsset{}, fmt.Errorf("发布资源 %s 大小无效或超过安全上限", name)
	}
	parsed, err := url.Parse(result.URL)
	if err != nil {
		return releaseAsset{}, errors.New("发布资源地址无效")
	}
	if err := validateDownloadHost(parsed); err != nil {
		return releaseAsset{}, err
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return releaseAsset{}, errors.New("发布资源地址包含不允许的参数")
	}
	expectedSuffix := "/releases/download/" + release.Tag + "/" + name
	valid := parsed.Host == "github.com" && repositoryPathMatches(parsed.Path, "/"+repository, expectedSuffix)
	if parsed.Host == "api.github.com" && result.ID > 0 {
		valid = repositoryPathMatches(parsed.Path, "/repos/"+repository, "/releases/assets/"+strconv.FormatInt(result.ID, 10))
	}
	if !valid {
		return releaseAsset{}, errors.New("发布资源必须属于所配置仓库的对应 Release")
	}
	return result, nil
}

func repositoryPathMatches(actual, repositoryPrefix, suffix string) bool {
	return strings.HasSuffix(actual, suffix) && strings.EqualFold(strings.TrimSuffix(actual, suffix), repositoryPrefix)
}

func releaseInstallReason(release latestRelease, repository, architecture string) string {
	name := "grok2api_" + release.Tag + "_linux_" + architecture + ".tar.gz"
	if _, err := findAsset(release, repository, name, maxArchiveBytes); err != nil {
		return err.Error()
	}
	if _, err := findAsset(release, repository, "checksums.txt", maxReleaseBytes); err != nil {
		return err.Error()
	}
	return ""
}

func (s *Service) download(ctx context.Context, asset releaseAsset, destination string, limit int64) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "grok2api/"+s.current)
	client := *s.client
	client.Timeout = 0 // The detached installation context bounds the entire operation.
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub 资源下载失败（HTTP %d）", response.StatusCode)
	}
	if response.ContentLength > limit {
		return errors.New("安装资源超过安全大小上限")
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	count, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if count > limit {
		return errors.New("安装资源超过安全大小上限")
	}
	if count != asset.Size {
		return errors.New("下载资源大小与 GitHub Release 不一致")
	}
	return nil
}

func checksumFor(data []byte, name string) ([]byte, error) {
	var checksum []byte
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if checksum != nil {
			return nil, errors.New("安装包存在重复的 SHA-256 校验和")
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return nil, errors.New("SHA-256 校验和格式无效")
		}
		checksum = decoded
	}
	if checksum == nil {
		return nil, fmt.Errorf("checksums.txt 中缺少 %s 的 SHA-256 校验和", name)
	}
	return checksum, nil
}

func verifyChecksum(filename string, expected []byte) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expected)) {
		return errors.New("安装包 SHA-256 校验失败")
	}
	return nil
}

func extractArchive(ctx context.Context, filename, destination string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("无效的 gzip 安装包: %w", err)
	}
	defer compressed.Close()
	archive := tar.NewReader(io.LimitReader(compressed, maxExtractedBytes+1))
	seen := make(map[string]bool)
	var total int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("读取安装包失败: %w", err)
		}
		if count >= maxArchiveFiles {
			return errors.New("安装包文件数量超过安全上限")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "../") || name == ".." {
			return fmt.Errorf("安装包包含不安全路径: %q", header.Name)
		}
		if seen[strings.ToLower(name)] {
			return fmt.Errorf("安装包包含重复路径: %s", name)
		}
		seen[strings.ToLower(name)] = true
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxArchiveBytes || total > maxExtractedBytes-header.Size {
				return errors.New("安装包解压大小超过安全上限")
			}
			total += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(output, archive, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("安装包禁止符号链接、硬链接及特殊文件: %s", name)
		}
	}
	return nil
}

func validateReleaseContents(directory, version, architecture string) error {
	for _, name := range []string{"grok2api", "VERSION", "frontend/dist/index.html"} {
		info, err := os.Lstat(filepath.Join(directory, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("安装包缺少有效的 %s", name)
		}
		if name == "VERSION" && info.Size() > 256 {
			return errors.New("安装包 VERSION 文件无效")
		}
	}
	data, err := os.ReadFile(filepath.Join(directory, "VERSION"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != version {
		return errors.New("安装包 VERSION 与 GitHub Release 标签不一致")
	}
	binaryPath := filepath.Join(directory, "grok2api")
	binary, err := elf.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("安装包不包含有效 Linux 可执行文件: %w", err)
	}
	defer binary.Close()
	machine := elf.EM_X86_64
	if architecture == "arm64" {
		machine = elf.EM_AARCH64
	}
	if binary.Class != elf.ELFCLASS64 || binary.Machine != machine || (binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) {
		return errors.New("安装包可执行文件架构不匹配")
	}
	return os.Chmod(binaryPath, 0755)
}
