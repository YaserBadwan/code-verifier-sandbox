package dockerhost

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeContext(t *testing.T, dir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	metaDir := filepath.Join(dir, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"Name":"` + name + `","Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDockerHostEnvWins(t *testing.T) {
	host, err := Resolve(getenv(map[string]string{"DOCKER_HOST": "tcp://1.2.3.4:2375", "DOCKER_CONTEXT": "x"}))
	if err != nil || host != "tcp://1.2.3.4:2375" {
		t.Fatalf("got %q %v", host, err)
	}
}

func TestCurrentContextFromConfig(t *testing.T) {
	dir := t.TempDir()
	writeContext(t, dir, "desktop-linux", "unix:///tmp/desktop.sock")
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{},"currentContext":"desktop-linux"}`), 0o644)

	host, err := Resolve(getenv(map[string]string{"DOCKER_CONFIG": dir}))
	if err != nil || host != "unix:///tmp/desktop.sock" {
		t.Fatalf("got %q %v", host, err)
	}
}

func TestDockerContextEnvOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	writeContext(t, dir, "other", "unix:///tmp/other.sock")
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"currentContext":"missing"}`), 0o644)

	host, err := Resolve(getenv(map[string]string{"DOCKER_CONFIG": dir, "DOCKER_CONTEXT": "other"}))
	if err != nil || host != "unix:///tmp/other.sock" {
		t.Fatalf("got %q %v", host, err)
	}
}

func TestFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	host, err := Resolve(getenv(map[string]string{"DOCKER_CONFIG": dir}))
	if err != nil || host != "" {
		t.Fatalf("no config should mean the SDK default, got %q %v", host, err)
	}

	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"currentContext":"default"}`), 0o644)
	host, err = Resolve(getenv(map[string]string{"DOCKER_CONFIG": dir}))
	if err != nil || host != "" {
		t.Fatalf("default context should mean the SDK default, got %q %v", host, err)
	}
}

func TestMissingContextIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"currentContext":"gone"}`), 0o644)
	if _, err := Resolve(getenv(map[string]string{"DOCKER_CONFIG": dir})); err == nil {
		t.Fatal("expected an error for a context without metadata")
	}
}

func TestBrokenConfigIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{nope`), 0o644)
	if _, err := Resolve(getenv(map[string]string{"DOCKER_CONFIG": dir})); err == nil {
		t.Fatal("expected an error for invalid json")
	}
}
