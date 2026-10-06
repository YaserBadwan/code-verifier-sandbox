package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/client"
)

// fullRef expands a short image name to its docker.io/library form.
// Newer daemons refuse to inspect the short form.
func fullRef(ref string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ref
	}
	return named.String()
}

type ImageAPI interface {
	ImageExists(ctx context.Context, ref string) (bool, error)
	PullImage(ctx context.Context, ref string) error
}

func (d *Docker) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, fullRef(ref))
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspecting image %s: %w", ref, err)
}

func (d *Docker) PullImage(ctx context.Context, ref string) error {
	resp, err := d.cli.ImagePull(ctx, fullRef(ref), client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}
	defer resp.Close()
	// the daemon reports failures inside the progress stream, not as an http error
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("pulling %s: %w", ref, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pulling %s: %s", ref, msg.Error.Message)
		}
	}
	return nil
}

type ImageState string

const (
	ImageMissing ImageState = "missing"
	ImagePulling ImageState = "pulling"
	ImageReady   ImageState = "ready"
	ImageFailed  ImageState = "failed"
)

// a failed pull is not retried more often than this, so being offline doesn't spam the registry
const pullRetryDelay = time.Minute

// Images tracks whether each language image is available locally.
type Images struct {
	api ImageAPI
	log *slog.Logger

	mu       sync.Mutex
	state    map[string]ImageState
	errs     map[string]error
	failedAt map[string]time.Time
	refs     []string
}

func NewImages(api ImageAPI, refs []string, log *slog.Logger) *Images {
	m := &Images{api: api, log: log, state: map[string]ImageState{}, errs: map[string]error{}, failedAt: map[string]time.Time{}}
	for _, ref := range refs {
		if !slices.Contains(m.refs, ref) {
			m.refs = append(m.refs, ref)
			m.state[ref] = ImageMissing
		}
	}
	return m
}

func (m *Images) Refs() []string { return slices.Clone(m.refs) }

// Refresh checks which images are already present.
func (m *Images) Refresh(ctx context.Context) error {
	var errs []error
	for _, ref := range m.refs {
		ok, err := m.api.ImageExists(ctx, ref)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m.mu.Lock()
		if ok {
			m.state[ref] = ImageReady
			delete(m.errs, ref)
		} else if m.state[ref] == ImageReady {
			// someone removed it, for example with docker image prune
			m.state[ref] = ImageMissing
		}
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// PullMissing downloads every image that is not present yet, one at a time.
func (m *Images) PullMissing(ctx context.Context) error {
	var errs []error
	for _, ref := range m.refs {
		m.mu.Lock()
		st := m.state[ref]
		recentlyFailed := st == ImageFailed && time.Since(m.failedAt[ref]) < pullRetryDelay
		if st == ImageReady || st == ImagePulling || recentlyFailed {
			m.mu.Unlock()
			continue
		}
		m.state[ref] = ImagePulling
		m.mu.Unlock()

		m.log.Info("pulling image, this can take a few minutes the first time", "image", ref)
		started := time.Now()
		err := m.api.PullImage(ctx, ref)

		m.mu.Lock()
		if err != nil {
			m.state[ref] = ImageFailed
			m.errs[ref] = err
			m.failedAt[ref] = time.Now()
			errs = append(errs, err)
			m.log.Error("image pull failed", "image", ref, "error", err)
		} else {
			m.state[ref] = ImageReady
			delete(m.errs, ref)
			m.log.Info("image ready", "image", ref, "took", time.Since(started).Round(time.Second).String())
		}
		m.mu.Unlock()
	}
	return errors.Join(errs...)
}

// Ready reports whether ref can be used right now. If it is not marked ready it
// checks Docker again, since someone may have pulled it by hand.
func (m *Images) Ready(ctx context.Context, ref string) bool {
	m.mu.Lock()
	st, known := m.state[ref]
	m.mu.Unlock()
	if !known {
		return false
	}
	if st == ImageReady {
		return true
	}
	if st == ImagePulling {
		return false
	}
	ok, err := m.api.ImageExists(ctx, ref)
	if err != nil || !ok {
		return false
	}
	m.mu.Lock()
	m.state[ref] = ImageReady
	m.mu.Unlock()
	return true
}

// MarkMissing forgets that ref was ready, so the next pull or check picks it up again.
func (m *Images) MarkMissing(ref string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, known := m.state[ref]; known && m.state[ref] != ImagePulling {
		m.state[ref] = ImageMissing
	}
}

func (m *Images) State(ref string) (ImageState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[ref], m.errs[ref]
}

func (m *Images) AllReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ref := range m.refs {
		if m.state[ref] != ImageReady {
			return false
		}
	}
	return true
}
