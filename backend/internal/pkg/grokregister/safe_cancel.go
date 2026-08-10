package grokregister

import "sync"

type SafeCancel struct {
	ch   chan struct{}
	once sync.Once
}

func NewSafeCancel() *SafeCancel {
	return &SafeCancel{ch: make(chan struct{})}
}

// NewChildCancel creates a cancel scope that follows parent cancellation without
// allowing a single registration job to stop the whole batch.
func NewChildCancel(parent *SafeCancel) *SafeCancel {
	child := NewSafeCancel()
	if parent == nil {
		return child
	}
	go func() {
		select {
		case <-parent.Done():
			child.Close()
		case <-child.Done():
		}
	}()
	return child
}

func (s *SafeCancel) Close() {
	s.once.Do(func() { close(s.ch) })
}

func (s *SafeCancel) Done() <-chan struct{} {
	return s.ch
}

func (s *SafeCancel) IsClosed() bool {
	select {
	case <-s.ch:
		return true
	default:
		return false
	}
}
