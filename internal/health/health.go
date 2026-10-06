// Package health answers whether the service can take submissions right now.
package health

import (
	"context"
	"time"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type ImageTracker interface {
	Refs() []string
	Refresh(ctx context.Context) error
	State(ref string) (sandbox.ImageState, error)
}

type Checker struct {
	docker Pinger
	images ImageTracker
}

type Report struct {
	Ready  bool              `json:"ready"`
	Docker string            `json:"docker"`
	Images map[string]string `json:"images"`
}

func NewChecker(docker Pinger, images ImageTracker) *Checker {
	return &Checker{docker: docker, images: images}
}

// Check is ready only when Docker answers and every enabled language image is present.
func (c *Checker) Check(ctx context.Context) Report {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	r := Report{Ready: true, Docker: "ok", Images: map[string]string{}}
	if err := c.docker.Ping(ctx); err != nil {
		r.Ready = false
		r.Docker = err.Error()
	}
	if r.Docker == "ok" {
		// asks Docker again, so images pulled or removed by hand show up here
		c.images.Refresh(ctx)
	}
	for _, ref := range c.images.Refs() {
		state, err := c.images.State(ref)
		text := string(state)
		if err != nil {
			text += ", " + err.Error()
		}
		r.Images[ref] = text
		if state != sandbox.ImageReady {
			r.Ready = false
		}
	}
	return r
}

// Watch calls onChange with the readiness every interval until ctx is done.
func (c *Checker) Watch(ctx context.Context, interval time.Duration, onChange func(Report)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		onChange(c.Check(ctx))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
