package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dotenvWorkspace(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, key := range []string{"PROJECTMAP_ADDR", "PROJECTMAP_DATABASE_URL", "PROJECTMAP_POSTGRES_PASSWORD"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func writeEnv(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(".env", []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFromDotenvAndExposeThroughGetenv(t *testing.T) {
	dotenvWorkspace(t)
	writeEnv(t, "# Local settings\nPROJECTMAP_ADDR=127.0.0.1:9090\nPROJECTMAP_DATABASE_URL='postgres://local/db?sslmode=disable'\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9090" || cfg.DatabaseURL != "postgres://local/db?sslmode=disable" {
		t.Fatal("file configuration was not loaded correctly")
	}
	if os.Getenv("PROJECTMAP_DATABASE_URL") != cfg.DatabaseURL {
		t.Fatal("dotenv values unavailable through os.Getenv")
	}
}

func TestEnvironmentOverridesDotenv(t *testing.T) {
	dotenvWorkspace(t)
	writeEnv(t, "PROJECTMAP_ADDR=127.0.0.1:9090\nPROJECTMAP_DATABASE_URL=postgres://file/db\n")
	t.Setenv("PROJECTMAP_DATABASE_URL", "postgres://environment/db")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://environment/db" || cfg.Addr != "127.0.0.1:9090" {
		t.Fatal("environment precedence or file fallback failed")
	}
	t.Setenv("PROJECTMAP_DATABASE_URL", "postgres://later/db")
	cfg, err = Load()
	if err != nil || cfg.DatabaseURL != "postgres://later/db" {
		t.Fatal("reloading replaced an explicit override")
	}
}

func TestExplicitlyEmptyEnvironmentIsNotReplaced(t *testing.T) {
	dotenvWorkspace(t)
	writeEnv(t, "PROJECTMAP_DATABASE_URL=postgres://file/db\n")
	t.Setenv("PROJECTMAP_DATABASE_URL", "")
	if _, err := Load(); err == nil || err.Error() != "PROJECTMAP_DATABASE_URL is required" {
		t.Fatal("explicit empty environment did not win over file")
	}
	if value, exists := os.LookupEnv("PROJECTMAP_DATABASE_URL"); !exists || value != "" {
		t.Fatal("empty override was mutated")
	}
}

func TestMissingDotenvUsesEnvironmentAndDefaults(t *testing.T) {
	dotenvWorkspace(t)
	t.Setenv("PROJECTMAP_DATABASE_URL", "postgres://environment/db")
	cfg, err := Load()
	if err != nil || cfg.Addr != "127.0.0.1:8080" {
		t.Fatal("missing optional dotenv prevented environment-only startup")
	}
}

func TestMissingRequiredConfiguration(t *testing.T) {
	dotenvWorkspace(t)
	if _, err := Load(); err == nil {
		t.Fatal("accepted missing required settings")
	}
}

func TestMalformedDotenvFailsWithoutLeakingOrPartialLoad(t *testing.T) {
	dotenvWorkspace(t)
	const secret = "private-password-that-must-not-appear"
	writeEnv(t, "PROJECTMAP_ADDR=127.0.0.1:9090\nPROJECTMAP_DATABASE_URL=\""+secret)
	t.Setenv("PROJECTMAP_DATABASE_URL", "postgres://environment/db")
	_, err := Load()
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "postgres://") {
		t.Fatal("malformed file was accepted or credentials exposed")
	}
	if os.Getenv("PROJECTMAP_ADDR") != "" {
		t.Fatal("malformed file partially changed the environment")
	}
	if os.Getenv("PROJECTMAP_DATABASE_URL") != "postgres://environment/db" {
		t.Fatal("malformed file replaced existing environment")
	}
}

func TestUnreadableDotenvFailsSafely(t *testing.T) {
	dotenvWorkspace(t)
	// A directory in place of the file reliably fails even for privileged users.
	if err := os.Mkdir(filepath.Join(".env"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnv(".env"); err == nil || err.Error() != "cannot read or parse .env file" {
		t.Fatal("file read failure was not sanitized")
	}
}
