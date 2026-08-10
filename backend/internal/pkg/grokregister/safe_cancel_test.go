package grokregister

import (
	"testing"
	"time"
)

func TestChildCancelScopes(t *testing.T) {
	parent := NewSafeCancel()
	child := NewChildCancel(parent)
	child.Close()
	if parent.IsClosed() {
		t.Fatal("closing a job scope must not cancel the batch")
	}

	child = NewChildCancel(parent)
	parent.Close()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not reach child")
	}
}
