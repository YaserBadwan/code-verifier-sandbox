// Package config loads server settings from flags and VERIFIER_* environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

type Config struct {
	GRPCAddr string
	HTTPAddr string
	// browser origins allowed to call the HTTP API, empty turns CORS off
	CORSOrigins []string

	// empty means every language is enabled
	Languages  []string
	PullImages bool
	PullOnly   bool
	Runtime    string
	// open the browser interface once the server is listening
	OpenBrowser bool

	MaxConcurrent int
	MaxQueue      int

	DefaultTimeLimit time.Duration
	MaxTimeLimit     time.Duration
	DefaultMemoryMB  int
	MaxMemoryMB      int
	CompileTimeout   time.Duration
	CompileMemoryMB  int
	OutputLimitKB    int
	MaxSourceKB      int
	MaxTests         int
	MaxRequestMB     int

	LogLevel        string
	LogFormat       string
	ShutdownTimeout time.Duration
}

// envName turns "max-tests" into "VERIFIER_MAX_TESTS"
func envName(flagName string) string {
	return "VERIFIER_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}

func newFlagSet(cfg *Config, languages, cors *string) *flag.FlagSet {
	fs := flag.NewFlagSet("verifier", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.GRPCAddr, "grpc-addr", "127.0.0.1:50051", "address for the gRPC API")
	fs.StringVar(&cfg.HTTPAddr, "http-addr", "127.0.0.1:8080", "address for the HTTP/JSON API")
	fs.StringVar(cors, "cors-origins", "", "comma separated browser origins allowed to call the HTTP API, or * for any")
	fs.StringVar(languages, "languages", "all", "comma separated language ids to enable, or all")
	fs.BoolVar(&cfg.PullImages, "pull-images", true, "pull missing language images in the background at startup")
	fs.BoolVar(&cfg.PullOnly, "pull-only", false, "pull the images for the enabled languages and exit")
	fs.BoolVar(&cfg.OpenBrowser, "open", false, "open the web interface in the default browser once the server is up")
	fs.StringVar(&cfg.Runtime, "runtime", "", "Docker runtime for sandbox containers, for example runsc for gVisor")

	fs.IntVar(&cfg.MaxConcurrent, "max-concurrent", 4, "submissions processed at the same time")
	fs.IntVar(&cfg.MaxQueue, "max-queue", 64, "submissions allowed to wait for a free slot before new ones are rejected")

	fs.DurationVar(&cfg.DefaultTimeLimit, "default-time-limit", 3*time.Second, "per test time limit when the request does not set one")
	fs.DurationVar(&cfg.MaxTimeLimit, "max-time-limit", 15*time.Second, "highest per test time limit a request may ask for")
	fs.IntVar(&cfg.DefaultMemoryMB, "default-memory-mb", 256, "per test memory limit when the request does not set one")
	fs.IntVar(&cfg.MaxMemoryMB, "max-memory-mb", 1024, "highest per test memory limit a request may ask for")
	fs.DurationVar(&cfg.CompileTimeout, "compile-timeout", 30*time.Second, "time limit for compiling one submission")
	fs.IntVar(&cfg.CompileMemoryMB, "compile-memory-mb", 1024, "memory limit for the compiler")
	fs.IntVar(&cfg.OutputLimitKB, "output-limit-kb", 1024, "stdout or stderr above this size fails the test with OUTPUT_LIMIT_EXCEEDED")
	fs.IntVar(&cfg.MaxSourceKB, "max-source-kb", 256, "largest accepted source file")
	fs.IntVar(&cfg.MaxTests, "max-tests", 100, "most test cases in one request")
	fs.IntVar(&cfg.MaxRequestMB, "max-request-mb", 32, "largest accepted request body")

	fs.StringVar(&cfg.LogLevel, "log-level", "info", "debug, info, warn or error")
	fs.StringVar(&cfg.LogFormat, "log-format", "text", "text or json")
	fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", 30*time.Second, "how long running submissions get to finish on shutdown")
	return fs
}

// Load applies defaults, then environment variables, then command line flags.
func Load(args []string, getenv func(string) string) (Config, error) {
	var cfg Config
	var languages, cors string
	fs := newFlagSet(&cfg, &languages, &cors)

	var envErr error
	fs.VisitAll(func(f *flag.Flag) {
		if envErr != nil {
			return
		}
		if v := getenv(envName(f.Name)); v != "" {
			if err := fs.Set(f.Name, v); err != nil {
				envErr = fmt.Errorf("invalid %s=%q: %w%s", envName(f.Name), v, err, formatHint(f))
			}
		}
	})
	if envErr != nil {
		return Config{}, envErr
	}

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	cfg.CORSOrigins = splitList(cors)
	cfg.Languages = parseLanguages(languages)
	if cfg.Languages == nil {
		return Config{}, errors.New("languages must be all or a list of language ids")
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func formatHint(f *flag.Flag) string {
	if getter, ok := f.Value.(flag.Getter); ok {
		if _, isDuration := getter.Get().(time.Duration); isDuration {
			return " (durations look like 500ms, 3s or 1m)"
		}
	}
	return ""
}

// returns an empty slice for "all" and nil when nothing usable was given
func parseLanguages(s string) []string {
	if strings.EqualFold(strings.TrimSpace(s), "all") {
		return []string{}
	}
	var out []string
	for _, p := range splitList(s) {
		out = append(out, strings.ToLower(p))
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c Config) validate() error {
	for name, addr := range map[string]string{"grpc-addr": c.GRPCAddr, "http-addr": c.HTTPAddr} {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("%s %q is not a host:port address", name, addr)
		}
	}
	if c.GRPCAddr == c.HTTPAddr {
		return errors.New("grpc-addr and http-addr cannot use the same address")
	}

	checks := []struct {
		ok  bool
		msg string
	}{
		{c.MaxConcurrent >= 1 && c.MaxConcurrent <= 64, "max-concurrent must be between 1 and 64"},
		{c.MaxQueue >= 0 && c.MaxQueue <= 10000, "max-queue must be between 0 and 10000"},
		{c.MaxTimeLimit >= 100*time.Millisecond && c.MaxTimeLimit <= 5*time.Minute, "max-time-limit must be between 100ms and 5m"},
		{c.DefaultTimeLimit >= 100*time.Millisecond && c.DefaultTimeLimit <= c.MaxTimeLimit, "default-time-limit must be at least 100ms and not above max-time-limit"},
		{c.MaxMemoryMB >= 32 && c.MaxMemoryMB <= 16384, "max-memory-mb must be between 32 and 16384"},
		{c.DefaultMemoryMB >= 32 && c.DefaultMemoryMB <= c.MaxMemoryMB, "default-memory-mb must be at least 32 and not above max-memory-mb"},
		{c.CompileTimeout >= time.Second && c.CompileTimeout <= 10*time.Minute, "compile-timeout must be between 1s and 10m"},
		{c.CompileMemoryMB >= 128 && c.CompileMemoryMB <= 16384, "compile-memory-mb must be between 128 and 16384"},
		{c.OutputLimitKB >= 1 && c.OutputLimitKB <= 64*1024, "output-limit-kb must be between 1 and 65536"},
		{c.MaxSourceKB >= 1 && c.MaxSourceKB <= 10*1024, "max-source-kb must be between 1 and 10240"},
		{c.MaxTests >= 1 && c.MaxTests <= 1000, "max-tests must be between 1 and 1000"},
		{c.MaxRequestMB >= 1 && c.MaxRequestMB <= 1024, "max-request-mb must be between 1 and 1024"},
		{c.ShutdownTimeout >= 0, "shutdown-timeout cannot be negative"},
		{oneOf(c.LogLevel, "debug", "info", "warn", "error"), "log-level must be debug, info, warn or error"},
		{oneOf(c.LogFormat, "text", "json"), "log-format must be text or json"},
	}
	for _, check := range checks {
		if !check.ok {
			return errors.New(check.msg)
		}
	}
	return nil
}

func oneOf(v string, options ...string) bool {
	for _, o := range options {
		if v == o {
			return true
		}
	}
	return false
}

// PrintUsage writes every flag with its environment variable and default.
func PrintUsage(w io.Writer) {
	var cfg Config
	var languages, cors string
	fs := newFlagSet(&cfg, &languages, &cors)
	fmt.Fprintf(w, "Usage of verifier\n\nEvery flag can also be set with the environment variable shown next to it.\nFlags win over environment variables.\n\n")
	fs.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(w, "  -%s  (%s)\n      %s (default %q)\n", f.Name, envName(f.Name), f.Usage, f.DefValue)
	})
}
