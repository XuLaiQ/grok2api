package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadUpdatePathsAndManagedFrontend(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`secrets:
  jwtSecret: "12345678901234567890123456789012"
  credentialEncryptionKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
bootstrapAdmin:
  password: "password123"
updates:
  directory: "./persistent/updates"
frontend:
  staticPath: "./frontend/dist"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DatabaseURLEnv, "")
	t.Setenv("GROK2API_UPDATE_DIR", "")
	t.Setenv("GROK2API_UPDATE_MANAGED", "")
	releaseFrontend := filepath.Join(directory, "release", "frontend", "dist")
	t.Setenv("GROK2API_FRONTEND_PATH", releaseFrontend)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Updates.Directory != filepath.Join(directory, "persistent", "updates") {
		t.Fatalf("update directory = %q", cfg.Updates.Directory)
	}
	if cfg.Frontend.StaticPath != filepath.Join(directory, "frontend", "dist") {
		t.Fatalf("unmanaged frontend override applied: %q", cfg.Frontend.StaticPath)
	}
	environmentDirectory := filepath.Join(directory, "volume", "updates")
	t.Setenv("GROK2API_UPDATE_DIR", environmentDirectory)
	t.Setenv("GROK2API_UPDATE_MANAGED", "1")
	cfg, err = Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Updates.Directory != environmentDirectory || cfg.Frontend.StaticPath != releaseFrontend {
		t.Fatalf("managed paths = %#v, %#v", cfg.Updates, cfg.Frontend)
	}
	if cfg.Database.SQLite.Path != filepath.Join(directory, "data", "backend.db") {
		t.Fatalf("database moved with release: %q", cfg.Database.SQLite.Path)
	}
}
