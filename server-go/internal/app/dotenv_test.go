package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(`
# comment
TEST_DOTENV_A=1
TEST_DOTENV_B = spaced

TEST_DOTENV_C=first
TEST_DOTENV_D="quoted value"
export TEST_DOTENV_E=exported
no-equals-line
=value-only
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_DOTENV_A", "preset")
	os.Unsetenv("TEST_DOTENV_B")
	os.Unsetenv("TEST_DOTENV_C")
	os.Unsetenv("TEST_DOTENV_D")
	os.Unsetenv("TEST_DOTENV_E")

	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("TEST_DOTENV_A"); got != "preset" {
		t.Errorf("preset var overridden: %q", got)
	}
	if got := os.Getenv("TEST_DOTENV_B"); got != "spaced" {
		t.Errorf("TEST_DOTENV_B = %q", got)
	}
	if got := os.Getenv("TEST_DOTENV_C"); got != "first" {
		t.Errorf("TEST_DOTENV_C = %q", got)
	}
	if got := os.Getenv("TEST_DOTENV_D"); got != "quoted value" {
		t.Errorf("TEST_DOTENV_D = %q", got)
	}
	if got := os.Getenv("TEST_DOTENV_E"); got != "exported" {
		t.Errorf("TEST_DOTENV_E = %q", got)
	}
}

func TestLoadDotEnvMissingFileIsNoop(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Fatalf("missing file must be a no-op, got %v", err)
	}
}
