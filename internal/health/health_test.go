package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
)

type pinger struct{ err error }

func (p pinger) Ping(context.Context) error { return p.err }

type images struct{ present map[string]bool }

func (i images) ImageExists(_ context.Context, ref string) (bool, error) { return i.present[ref], nil }
func (i images) PullImage(context.Context, string) error                 { return errors.New("offline") }

func tracker(present map[string]bool, refs ...string) *sandbox.Images {
	m := sandbox.NewImages(images{present}, refs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Refresh(context.Background())
	return m
}

func TestReady(t *testing.T) {
	c := NewChecker(pinger{}, tracker(map[string]bool{"a": true, "b": true}, "a", "b"))
	r := c.Check(context.Background())
	if !r.Ready || r.Docker != "ok" || r.Images["a"] != "ready" {
		t.Fatalf("expected ready, got %+v", r)
	}
}

func TestDockerDown(t *testing.T) {
	c := NewChecker(pinger{errors.New("connection refused")}, tracker(map[string]bool{"a": true}, "a"))
	r := c.Check(context.Background())
	if r.Ready || !strings.Contains(r.Docker, "refused") {
		t.Fatalf("expected not ready, got %+v", r)
	}
}

func TestMissingAndFailedImages(t *testing.T) {
	m := tracker(map[string]bool{}, "a", "b")
	m.PullMissing(context.Background())
	r := NewChecker(pinger{}, m).Check(context.Background())
	if r.Ready || !strings.HasPrefix(r.Images["a"], "failed") || !strings.Contains(r.Images["a"], "offline") {
		t.Fatalf("expected failed images in report, got %+v", r)
	}
}
