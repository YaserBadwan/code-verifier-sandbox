package sandbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
)

type fakeImageAPI struct {
	mu      sync.Mutex
	present map[string]bool
	failing map[string]bool
	pulled  []string
}

func (f *fakeImageAPI) ImageExists(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present[ref], nil
}

func (f *fakeImageAPI) PullImage(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, ref)
	if f.failing[ref] {
		return errors.New("manifest unknown")
	}
	f.present[ref] = true
	return nil
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestImagesPullOnlyMissing(t *testing.T) {
	api := &fakeImageAPI{present: map[string]bool{"a": true}, failing: map[string]bool{}}
	m := NewImages(api, []string{"a", "b", "b"}, quietLog)
	ctx := context.Background()

	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if m.AllReady() {
		t.Fatal("b is missing so not everything is ready")
	}
	if err := m.PullMissing(ctx); err != nil {
		t.Fatal(err)
	}
	if len(api.pulled) != 1 || api.pulled[0] != "b" {
		t.Fatalf("expected only b to be pulled once, got %v", api.pulled)
	}
	if !m.AllReady() || !m.Ready(ctx, "b") {
		t.Fatal("everything should be ready after pulling")
	}
}

func TestImagesPullFailureIsReported(t *testing.T) {
	api := &fakeImageAPI{present: map[string]bool{}, failing: map[string]bool{"bad": true}}
	m := NewImages(api, []string{"bad"}, quietLog)
	if err := m.PullMissing(context.Background()); err == nil {
		t.Fatal("expected the pull error")
	}
	st, err := m.State("bad")
	if st != ImageFailed || err == nil {
		t.Fatalf("expected failed state with error, got %s %v", st, err)
	}
	if m.Ready(context.Background(), "bad") {
		t.Fatal("failed image should not be ready")
	}
}

func TestImagesReadyNoticesManualPull(t *testing.T) {
	api := &fakeImageAPI{present: map[string]bool{}, failing: map[string]bool{}}
	m := NewImages(api, []string{"x"}, quietLog)
	ctx := context.Background()
	m.Refresh(ctx)
	if m.Ready(ctx, "x") {
		t.Fatal("x is not there yet")
	}
	api.present["x"] = true
	if !m.Ready(ctx, "x") {
		t.Fatal("Ready should check docker again and see the image")
	}
	if m.Ready(ctx, "unknown") {
		t.Fatal("an image that is not tracked is never ready")
	}
}

func TestRefreshNoticesRemovedImage(t *testing.T) {
	api := &fakeImageAPI{present: map[string]bool{"a": true}, failing: map[string]bool{}}
	m := NewImages(api, []string{"a"}, quietLog)
	ctx := context.Background()
	m.Refresh(ctx)
	if !m.AllReady() {
		t.Fatal("a should be ready")
	}
	api.present["a"] = false
	m.Refresh(ctx)
	if st, _ := m.State("a"); st != ImageMissing {
		t.Fatalf("a removed image should go back to missing, got %s", st)
	}
	m.PullMissing(ctx)
	if !m.AllReady() {
		t.Fatal("pulling again should bring it back")
	}
}

func TestFailedPullIsNotRetriedRightAway(t *testing.T) {
	api := &fakeImageAPI{present: map[string]bool{}, failing: map[string]bool{"x": true}}
	m := NewImages(api, []string{"x"}, quietLog)
	m.PullMissing(context.Background())
	m.PullMissing(context.Background())
	if len(api.pulled) != 1 {
		t.Fatalf("expected a single attempt within the retry delay, got %d", len(api.pulled))
	}
}

func TestMarkMissing(t *testing.T) {
	api := &fakeImageAPI{present: map[string]bool{"a": true}, failing: map[string]bool{}}
	m := NewImages(api, []string{"a"}, quietLog)
	m.Refresh(context.Background())
	api.present["a"] = false
	m.MarkMissing("a")
	if m.Ready(context.Background(), "a") {
		t.Fatal("a should not be ready after MarkMissing when docker no longer has it")
	}
}
