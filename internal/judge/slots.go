package judge

import (
	"context"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// slots limits how many submissions run at once and how many may wait in line.
type slots struct {
	sem      chan struct{}
	waiting  atomic.Int64
	maxQueue int64
}

func newSlots(concurrent, queue int) *slots {
	return &slots{sem: make(chan struct{}, max(concurrent, 1)), maxQueue: int64(max(queue, 0))}
}

func (s *slots) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	default:
	}
	if s.waiting.Add(1) > s.maxQueue {
		s.waiting.Add(-1)
		return status.Error(codes.ResourceExhausted, "the server is busy, too many submissions are waiting, try again shortly")
	}
	defer s.waiting.Add(-1)
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

func (s *slots) release() { <-s.sem }
