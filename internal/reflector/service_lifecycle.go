package reflector

import (
	"context"
	"errors"
	"sync"
)

// serviceLife registers Serve before shutdown can join it. The serving
// goroutine shuts down resources but never waits for its own completion.
//
//declscope:package // Light and FullService share the same ownership rules
type serviceLife struct {
	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
	err     error
}

// begin registers the one serving goroutine.
//
//declscope:package // shared by Light and FullService lifecycle methods
func (s *serviceLife) begin(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, nil, errors.New("Serve called twice")
	}
	if s.closed {
		return nil, nil, errors.New("reflector is closed")
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.done = make(chan struct{})
	return ctx, func() { close(s.done) }, nil
}

// shutdown cancels serving and releases owned sockets once.
//
//declscope:package // shared by Light and FullService lifecycle methods
func (s *serviceLife) shutdown(closeSockets func() error) error {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.once.Do(func() { s.err = closeSockets() })
	return s.err
}

// wait joins an already registered serving goroutine.
//
//declscope:package // shared by Light and FullService lifecycle methods
func (s *serviceLife) wait() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
