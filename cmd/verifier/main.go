// Command verifier runs the code verification service with a gRPC and an HTTP/JSON API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/api/grpcapi"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/api/httpapi"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/config"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/dockerhost"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/health"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/instancelock"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/judge"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
	"github.com/YaserBadwan/code-verifier-sandbox/web"
)

// how much of each test's stdout and stderr goes back to the client
const reportBytes = 64 << 10

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if errors.Is(err, flag.ErrHelp) {
		config.PrintUsage(os.Stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "verifier: %v\nrun `verifier -h` to see every option\n", err)
		return 2
	}
	log := newLogger(cfg)

	langs, err := languages.Select(cfg.Languages)
	if err != nil {
		log.Error("bad language list", "error", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	release, err := instancelock.Acquire()
	if err != nil {
		log.Error("could not start", "error", err)
		return 1
	}
	defer release()

	docker, err := connectDocker(ctx, cfg, log)
	if err != nil {
		log.Error("could not start", "error", err)
		return 1
	}
	defer docker.Close()

	images := sandbox.NewImages(docker, imageRefs(langs), log)
	if err := images.Refresh(ctx); err != nil {
		log.Warn("could not check local images", "error", err)
	}

	if cfg.PullOnly {
		if err := images.PullMissing(ctx); err != nil {
			log.Error("some images could not be pulled", "error", err)
			return 1
		}
		log.Info("all images are ready", "images", strings.Join(images.Refs(), ", "))
		return 0
	}

	if c, v, err := docker.RemoveLeftovers(ctx); err != nil {
		log.Warn("could not clean up leftovers from a previous run", "error", err)
	} else if c+v > 0 {
		log.Info("removed leftovers from a previous run", "containers", c, "volumes", v)
	}

	if !images.AllReady() {
		if cfg.PullImages {
			go images.PullMissing(ctx)
		} else {
			log.Warn("some language images are missing and pulling is off, those languages will be unavailable until you run `make images`")
		}
	}

	j := judge.New(judge.Config{
		Runner:    docker,
		Images:    images,
		Languages: langs,
		Limits: judge.Limits{
			DefaultTimeLimit: cfg.DefaultTimeLimit,
			MaxTimeLimit:     cfg.MaxTimeLimit,
			DefaultMemoryMB:  cfg.DefaultMemoryMB,
			MaxMemoryMB:      cfg.MaxMemoryMB,
			CompileTimeout:   cfg.CompileTimeout,
			CompileMemoryMB:  cfg.CompileMemoryMB,
			OutputLimitBytes: int64(cfg.OutputLimitKB) << 10,
			ReportBytes:      reportBytes,
			MaxSourceBytes:   cfg.MaxSourceKB << 10,
			MaxTests:         cfg.MaxTests,
		},
		MaxConcurrent: cfg.MaxConcurrent,
		MaxQueue:      cfg.MaxQueue,
		Log:           log,
	})
	return serve(ctx, stop, cfg, j, docker, images, log)
}

// responses carry stdout and stderr for every test, so size the gRPC send limit from that
func maxResponseBytes(cfg config.Config) int {
	perTest := 2*reportBytes + 1024
	return max(2*(cfg.MaxRequestMB<<20), cfg.MaxTests*perTest+(1<<20))
}

func serve(ctx context.Context, stop context.CancelFunc, cfg config.Config, j *judge.Service, docker *sandbox.Docker, images *sandbox.Images, log *slog.Logger) int {
	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Error("could not open the gRPC port, is another verifier already running?", "addr", cfg.GRPCAddr, "error", err)
		return 1
	}
	httpLis, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		grpcLis.Close()
		log.Error("could not open the HTTP port, is another verifier already running?", "addr", cfg.HTTPAddr, "error", err)
		return 1
	}

	checker := health.NewChecker(docker, images)
	logReadiness := readinessLogger(log)
	maxBody := cfg.MaxRequestMB << 20
	grpcSrv, grpcHealth := grpcapi.New(j, log, maxBody, maxResponseBytes(cfg))

	// http.Server.Close does not wait for handlers, this does
	var inFlight sync.WaitGroup
	api := httpapi.New(httpapi.Options{
		Judge:        j,
		Health:       checker.Check,
		Log:          log,
		MaxBodyBytes: int64(maxBody),
		CORSOrigins:  cfg.CORSOrigins,
		AllowedHosts: allowedHosts(cfg.HTTPAddr),
		UI:           web.Static(),
	})
	httpSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inFlight.Add(1)
			defer inFlight.Done()
			api.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// covers reading the body only, a long running verification is not cut off by it
		ReadTimeout: time.Minute,
		IdleTimeout: 2 * time.Minute,
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	go checker.Watch(ctx, 5*time.Second, func(r health.Report) {
		logReadiness(r)
		grpcapi.SetReady(grpcHealth, r.Ready)
		// brings back images that were pruned while we were running
		if !r.Ready && cfg.PullImages && r.Docker == "ok" && ctx.Err() == nil {
			go images.PullMissing(ctx)
		}
	})

	errs := make(chan error, 2)
	go func() { errs <- grpcSrv.Serve(grpcLis) }()
	go func() {
		if err := httpSrv.Serve(httpLis); !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	pageURL := browserURL(httpLis.Addr())
	log.Info("verifier is running", "web", pageURL, "grpc", grpcLis.Addr().String(), "languages", languageIDs(j))
	if cfg.OpenBrowser {
		if err := openBrowser(pageURL); err != nil {
			log.Warn("could not open the browser, open the address yourself", "url", pageURL, "error", err)
		}
	}

	exitCode := 0
	select {
	case <-ctx.Done():
		log.Info("shutting down, waiting for running submissions to finish", "timeout", cfg.ShutdownTimeout.String())
	case err := <-errs:
		log.Error("server stopped unexpectedly", "error", err)
		exitCode = 1
	}
	// a second Ctrl+C now kills the process right away
	stop()
	grpcapi.SetReady(grpcHealth, false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	grpcDone := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(grpcDone)
	}()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		// closing cancels the request contexts, which kills their containers
		httpSrv.Close()
	}
	select {
	case <-grpcDone:
	case <-shutdownCtx.Done():
		grpcSrv.Stop()
		<-grpcDone
	}
	// handlers notice the closed connection quickly and remove their own containers
	waitTimeout(&inFlight, 30*time.Second)

	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCleanup()
	if c, v, err := docker.RemoveLeftovers(cleanupCtx); err != nil {
		log.Warn("cleanup on shutdown failed", "error", err)
	} else if c+v > 0 {
		log.Info("cleaned up after shutdown", "containers", c, "volumes", v)
	}
	log.Info("bye")
	return exitCode
}

func connectDocker(ctx context.Context, cfg config.Config, log *slog.Logger) (*sandbox.Docker, error) {
	host, err := dockerhost.Resolve(os.Getenv)
	if err != nil {
		return nil, err
	}
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	docker, err := sandbox.NewDocker(connectCtx, host, cfg.Runtime, log)
	if err != nil {
		return nil, err
	}
	runtimeName := cfg.Runtime
	if runtimeName == "" {
		runtimeName = "default"
	}
	log.Info("connected to docker", "host", docker.Host(), "runtime", runtimeName)
	return docker, nil
}

// browserURL turns the listen address into something a browser can open
func browserURL(addr net.Addr) string {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.IP.IsUnspecified() {
		return fmt.Sprintf("http://127.0.0.1:%d", tcpPort(addr))
	}
	return "http://" + addr.String()
}

func tcpPort(addr net.Addr) int {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 8080
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// reap it in the background so it doesn't linger as a zombie
	go cmd.Wait()
	return nil
}

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// loopback only setups get a Host check against DNS rebinding. If the operator binds to
// another interface on purpose we can't know which names clients will use.
func allowedHosts(addr string) []string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return []string{"localhost", "127.0.0.1", "::1", strings.ToLower(host)}
	}
	return nil
}

// only logs when readiness actually changes, otherwise this would spam every few seconds
func readinessLogger(log *slog.Logger) func(health.Report) {
	first := true
	var wasReady bool
	return func(r health.Report) {
		if !first && r.Ready == wasReady {
			return
		}
		first = false
		wasReady = r.Ready
		if r.Ready {
			log.Info("ready to take submissions")
			return
		}
		var waiting []string
		for ref, state := range r.Images {
			if state != string(sandbox.ImageReady) {
				waiting = append(waiting, ref+" "+state)
			}
		}
		slices.Sort(waiting)
		log.Warn("not ready yet", "docker", r.Docker, "images", strings.Join(waiting, "; "))
	}
}

func imageRefs(langs []languages.Language) []string {
	var refs []string
	for _, l := range langs {
		if !slices.Contains(refs, l.Image) {
			refs = append(refs, l.Image)
		}
	}
	return refs
}

func languageIDs(j *judge.Service) string {
	var ids []string
	for _, l := range j.Languages(context.Background()) {
		ids = append(ids, l.Id)
	}
	return strings.Join(ids, ",")
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
