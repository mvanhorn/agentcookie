package cookiesource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpen_EnvUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	src, err := Open()
	if err != nil {
		t.Errorf("unset env: got err %v, want nil", err)
	}
	if src.PlainText {
		t.Error("unset env: PlainText should be false")
	}
	if src.Path != "" {
		t.Errorf("unset env: Path should be empty, got %q", src.Path)
	}
	if Available() {
		t.Error("Available should be false when env unset")
	}
}

func TestOpen_EnvSetFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies-plain.db")
	if err := os.WriteFile(path, []byte("sqlite3-bytes-here"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvVar, path)

	src, err := Open()
	if err != nil {
		t.Errorf("happy path: got err %v, want nil", err)
	}
	if !src.PlainText {
		t.Error("happy path: PlainText should be true")
	}
	if src.Path != path {
		t.Errorf("happy path: Path %q, want %q", src.Path, path)
	}
	if !Available() {
		t.Error("Available should be true when env set + file exists")
	}
}

func TestOpen_EnvSetFileMissing(t *testing.T) {
	t.Setenv(EnvVar, "/tmp/this-does-not-exist-agentcookie-test-1234")
	src, err := Open()
	if !errors.Is(err, ErrSidecarMissing) {
		t.Errorf("missing-file: got err %v, want ErrSidecarMissing", err)
	}
	if !src.PlainText {
		t.Error("missing-file should still report PlainText=true so caller knows the bridge was meant to be used")
	}
	if Available() {
		t.Error("Available should be false when sidecar missing")
	}
}

func TestOpen_EnvSetPointsAtDirectory(t *testing.T) {
	t.Setenv(EnvVar, t.TempDir())
	_, err := Open()
	if err == nil {
		t.Error("dir path: expected error, got nil")
	}
}
