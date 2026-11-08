package settings

import (
	"context"
	"testing"
	"time"

	settingsdomain "github.com/chenyme/grok2api/backend/internal/domain/settings"
	"github.com/chenyme/grok2api/backend/internal/infra/config"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestUpgradeBuildClientVersionPersistsAndAppliesFingerprintPair(t *testing.T) {
	cfg := testConfig(t)
	cfg.Provider.Build.ClientVersion = "1.0.4"
	cfg.Provider.Build.UserAgent = "grok-shell/1.0.4 (linux; x86_64)"
	repo := &runtimeSettingsRepositoryStub{}
	var applied config.Config
	notifications := 0
	service := NewService(cfg, time.Time{}, 0, repo, func(context.Context) { notifications++ }, func(next config.Config) { applied = next })

	version, changed, err := service.UpgradeBuildClientVersion(context.Background(), "1.0.60")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || version != "1.0.60" {
		t.Fatalf("upgrade = %q, %t", version, changed)
	}
	for _, value := range []config.Config{applied, repoConfig(repo, cfg)} {
		if value.Provider.Build.ClientVersion != "1.0.60" || value.Provider.Build.UserAgent != buildUserAgentForTest("1.0.60") {
			t.Fatalf("Build fingerprint was not upgraded atomically: %#v", value.Provider.Build)
		}
	}
	if repo.revision != 1 || notifications != 1 {
		t.Fatalf("revision=%d notifications=%d", repo.revision, notifications)
	}

	loaded, _, _, err := LoadPersisted(context.Background(), cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provider.Build.ClientVersion != "1.0.60" || loaded.Provider.Build.UserAgent != buildUserAgentForTest("1.0.60") {
		t.Fatalf("persisted Build fingerprint = %#v", loaded.Provider.Build)
	}
}

func TestUpgradeBuildClientVersionIgnoresEqualOrLowerVersions(t *testing.T) {
	cfg := testConfig(t)
	cfg.Provider.Build.ClientVersion = "1.0.60"
	cfg.Provider.Build.UserAgent = buildUserAgentForTest("1.0.60")
	repo := &runtimeSettingsRepositoryStub{}
	service := NewService(cfg, time.Time{}, 0, repo, nil, nil)
	for _, version := range []string{"1.0.60", "1.0.13"} {
		got, changed, err := service.UpgradeBuildClientVersion(context.Background(), version)
		if err != nil || changed || got != "1.0.60" {
			t.Fatalf("UpgradeBuildClientVersion(%q) = %q, %t, %v", version, got, changed, err)
		}
	}
	if repo.found || repo.revision != 0 {
		t.Fatalf("non-upgrade changed persisted state: found=%t revision=%d", repo.found, repo.revision)
	}
}

type conflictOnceSettingsRepository struct {
	*runtimeSettingsRepositoryStub
	conflictValue settingsdomain.Config
	conflictOnce  bool
}

func (r *conflictOnceSettingsRepository) Save(ctx context.Context, value settingsdomain.Config, expectedRevision uint64) (time.Time, uint64, error) {
	if !r.conflictOnce {
		r.conflictOnce = true
		r.value = r.conflictValue
		r.updatedAt = time.Now().UTC()
		r.revision++
		r.found = true
		return time.Time{}, 0, repository.ErrConflict
	}
	return r.runtimeSettingsRepositoryStub.Save(ctx, value, expectedRevision)
}

func TestUpgradeBuildClientVersionRetriesAfterConcurrentSettingsChange(t *testing.T) {
	cfg := testConfig(t)
	cfg.Provider.Build.ClientVersion = "1.0.4"
	cfg.Provider.Build.UserAgent = buildUserAgentForTest("1.0.4")
	base := &runtimeSettingsRepositoryStub{}
	conflictConfig := cfg
	conflictConfig.Provider.Build.ClientVersion = "1.0.13"
	conflictConfig.Provider.Build.UserAgent = buildUserAgentForTest("1.0.13")
	repo := &conflictOnceSettingsRepository{runtimeSettingsRepositoryStub: base, conflictValue: toDomainConfig(conflictConfig)}
	var applied config.Config
	service := NewService(cfg, time.Time{}, 0, repo, nil, func(next config.Config) { applied = next })
	version, changed, err := service.UpgradeBuildClientVersion(context.Background(), "1.0.60")
	if err != nil || !changed || version != "1.0.60" {
		t.Fatalf("upgrade after conflict = %q, %t, %v", version, changed, err)
	}
	if repo.revision != 2 || base.value.ProviderBuild.ClientVersion != "1.0.60" || applied.Provider.Build.UserAgent != buildUserAgentForTest("1.0.60") {
		t.Fatalf("conflict retry did not persist and apply the higher version: revision=%d stored=%q applied=%q", repo.revision, base.value.ProviderBuild.ClientVersion, applied.Provider.Build.UserAgent)
	}
}

func TestUpgradeBuildClientVersionRejectsInvalidVersions(t *testing.T) {
	service := NewService(testConfig(t), time.Time{}, 0, &runtimeSettingsRepositoryStub{}, nil, nil)
	for _, version := range []string{"v1.0.60", "1.0.60-beta.1", "latest", "1.0"} {
		if _, _, err := service.UpgradeBuildClientVersion(context.Background(), version); err == nil {
			t.Fatalf("invalid version %q accepted", version)
		}
	}
}

func TestRecommendedBuildFingerprintDoesNotRegress(t *testing.T) {
	cfg := testConfig(t)
	cfg.Provider.Build.ClientVersion = "1.0.60"
	cfg.Provider.Build.UserAgent = buildUserAgentForTest("1.0.60")
	recommendation := NewService(cfg, time.Time{}, 0, &runtimeSettingsRepositoryStub{}, nil, nil).Get().RecommendedProviderBuild
	if recommendation.ClientVersion != "1.0.60" || recommendation.UserAgent != buildUserAgentForTest("1.0.60") {
		t.Fatalf("recommended fingerprint regressed: %#v", recommendation)
	}
}

func repoConfig(repo *runtimeSettingsRepositoryStub, base config.Config) config.Config {
	return applyDomainConfig(base, repo.value)
}

func buildUserAgentForTest(version string) string {
	return "grok-shell/" + version + " (linux; x86_64)"
}
