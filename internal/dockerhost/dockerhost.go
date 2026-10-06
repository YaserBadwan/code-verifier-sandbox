// Package dockerhost figures out which Docker daemon to talk to, the same way the docker CLI does.
package dockerhost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Resolve returns the daemon address, or "" when the SDK default socket should be used.
// The SDK alone only reads DOCKER_HOST, which breaks Docker Desktop on macOS where the
// socket lives under the home directory and is selected through a docker context.
func Resolve(getenv func(string) string) (string, error) {
	if host := getenv("DOCKER_HOST"); host != "" {
		return host, nil
	}

	configDir := getenv("DOCKER_CONFIG")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		configDir = filepath.Join(home, ".docker")
	}

	name := getenv("DOCKER_CONTEXT")
	if name == "" {
		var err error
		name, err = currentContext(configDir)
		if err != nil {
			return "", err
		}
	}
	if name == "" || name == "default" {
		return "", nil
	}
	return contextHost(configDir, name)
}

func currentContext(configDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("reading %s: %w", filepath.Join(configDir, "config.json"), err)
	}
	return cfg.CurrentContext, nil
}

// the CLI stores each context under a folder named after the sha256 of its name
func contextHost(configDir, name string) (string, error) {
	sum := sha256.Sum256([]byte(name))
	path := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("docker context %q is selected but its metadata could not be read (try `docker context use default`): %w", name, err)
	}
	var meta struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	host := meta.Endpoints["docker"].Host
	if host == "" {
		return "", fmt.Errorf("docker context %q has no docker endpoint", name)
	}
	return host, nil
}
