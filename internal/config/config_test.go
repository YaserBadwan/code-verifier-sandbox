package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCAddr != "127.0.0.1:50051" || cfg.HTTPAddr != "127.0.0.1:8080" {
		t.Fatalf("unexpected default addresses %q %q", cfg.GRPCAddr, cfg.HTTPAddr)
	}
	if cfg.DefaultTimeLimit != 3*time.Second || cfg.DefaultMemoryMB != 256 {
		t.Fatalf("unexpected default limits %v %d", cfg.DefaultTimeLimit, cfg.DefaultMemoryMB)
	}
	if !cfg.PullImages || cfg.PullOnly {
		t.Fatal("expected pull-images on and pull-only off by default")
	}
	if len(cfg.Languages) != 0 {
		t.Fatalf("expected all languages by default, got %v", cfg.Languages)
	}
}

func TestEnvThenFlagPrecedence(t *testing.T) {
	e := env(map[string]string{
		"VERIFIER_HTTP_ADDR":      "127.0.0.1:9000",
		"VERIFIER_MAX_CONCURRENT": "2",
		"VERIFIER_LANGUAGES":      "python, cpp",
	})
	cfg, err := Load([]string{"-max-concurrent", "3"}, e)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9000" {
		t.Fatalf("env not applied, got %q", cfg.HTTPAddr)
	}
	if cfg.MaxConcurrent != 3 {
		t.Fatalf("flag should win over env, got %d", cfg.MaxConcurrent)
	}
	if strings.Join(cfg.Languages, ",") != "python,cpp" {
		t.Fatalf("languages parsed wrong %v", cfg.Languages)
	}
}

func TestCORSOrigins(t *testing.T) {
	cfg, err := Load(nil, env(map[string]string{"VERIFIER_CORS_ORIGINS": "http://localhost:3000, http://127.0.0.1:5173"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CORSOrigins) != 2 || cfg.CORSOrigins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("got %v", cfg.CORSOrigins)
	}
	if def, _ := Load(nil, env(nil)); len(def.CORSOrigins) != 0 {
		t.Fatal("CORS should be off by default")
	}
}

func TestLanguagesAll(t *testing.T) {
	cfg, err := Load([]string{"-languages", "ALL"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Languages) != 0 {
		t.Fatalf("all should mean no filter, got %v", cfg.Languages)
	}
}

func TestInvalid(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"bad env int", nil, map[string]string{"VERIFIER_MAX_TESTS": "lots"}, "VERIFIER_MAX_TESTS"},
		{"bad env duration", nil, map[string]string{"VERIFIER_COMPILE_TIMEOUT": "10"}, "durations look like"},
		{"unknown flag", []string{"-nope"}, nil, "nope"},
		{"same addr", []string{"-http-addr", "127.0.0.1:50051"}, nil, "same address"},
		{"bad addr", []string{"-grpc-addr", "localhost"}, nil, "grpc-addr"},
		{"default above max time", []string{"-default-time-limit", "20s", "-max-time-limit", "10s"}, nil, "default-time-limit"},
		{"tiny time", []string{"-default-time-limit", "1ms"}, nil, "default-time-limit"},
		{"default above max mem", []string{"-default-memory-mb", "512", "-max-memory-mb", "256"}, nil, "default-memory-mb"},
		{"zero concurrency", []string{"-max-concurrent", "0"}, nil, "max-concurrent"},
		{"bad log level", []string{"-log-level", "loud"}, nil, "log-level"},
		{"bad log format", []string{"-log-format", "xml"}, nil, "log-format"},
		{"empty languages", []string{"-languages", " , "}, nil, "languages"},
		{"leftover args", []string{"extra"}, nil, "unexpected argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.args, env(tc.env))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
		})
	}
}
