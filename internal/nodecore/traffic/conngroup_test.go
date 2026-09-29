package traffic

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type closeCounter struct{ n atomic.Int32 }

func (c *closeCounter) Close() error { c.n.Add(1); return nil }

func TestConnGroupCloseAllClosesEveryHeldConnection(t *testing.T) {
	g := NewConnGroup()
	conns := make([]*closeCounter, 200) // more than the close workers, to exercise the pool
	for i := range conns {
		conns[i] = new(closeCounter)
		g.Hold(conns[i])
	}
	if g.Len() != len(conns) {
		t.Fatalf("Len = %d, want %d", g.Len(), len(conns))
	}

	g.CloseAll()

	for i, c := range conns {
		if got := c.n.Load(); got != 1 {
			t.Fatalf("connection %d closed %d times, want once", i, got)
		}
	}
	if g.Len() != 0 {
		t.Errorf("Len after CloseAll = %d, want 0", g.Len())
	}
	g.CloseAll() // harmless the second time
}

func TestConnGroupReleaseForgetsTheConnection(t *testing.T) {
	g := NewConnGroup()
	kept, released := new(closeCounter), new(closeCounter)
	g.Hold(kept)
	release := g.Hold(released)
	release()
	release() // idempotent

	g.CloseAll()

	if kept.n.Load() != 1 {
		t.Errorf("held connection closed %d times, want once", kept.n.Load())
	}
	if released.n.Load() != 0 {
		t.Errorf("released connection closed %d times, want never: it had already ended", released.n.Load())
	}
}

func TestConnGroupRefusesConnectionsAfterCloseAll(t *testing.T) {
	g := NewConnGroup()
	g.CloseAll()

	late := new(closeCounter)
	release := g.Hold(late) // a handshake that finished after its core was closed

	if late.n.Load() != 1 {
		t.Errorf("late connection closed %d times, want once", late.n.Load())
	}
	if g.Len() != 0 {
		t.Errorf("Len = %d, want 0: a closed group holds nothing", g.Len())
	}
	release() // must not panic
}

func TestConnGroupTrackChainsOnClose(t *testing.T) {
	g := NewConnGroup()
	c := new(closeCounter)
	var got error
	onClose := g.Track(c, func(err error) { got = err })

	if g.Len() != 1 {
		t.Fatalf("Len = %d, want 1", g.Len())
	}
	want := errors.New("done")
	onClose(want)
	if got != want {
		t.Errorf("wrapped onClose got %v, want %v", got, want)
	}
	if g.Len() != 0 {
		t.Errorf("Len after onClose = %d, want 0", g.Len())
	}

	// A nil onClose is allowed.
	g.Track(c, nil)(nil)
}

func TestNilConnGroupTracksNothing(t *testing.T) {
	var g *ConnGroup
	c := new(closeCounter)
	g.Hold(c)()
	called := false
	g.Track(c, func(error) { called = true })(nil)
	g.CloseAll()
	if !called {
		t.Error("nil group swallowed the caller's onClose")
	}
	if g.Len() != 0 || c.n.Load() != 0 {
		t.Errorf("nil group changed state: Len=%d closes=%d", g.Len(), c.n.Load())
	}
}

func TestConnGroupIsSafeUnderConcurrentUse(t *testing.T) {
	g := NewConnGroup()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				g.Hold(new(closeCounter))()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			g.Hold(new(closeCounter))
		}
		g.CloseAll()
	}()
	wg.Wait()
	if g.Len() != 0 {
		t.Errorf("Len = %d, want 0 after CloseAll", g.Len())
	}
}

// blockedCloser is a connection whose Close never returns, like a TLS close
// toward a peer that has stopped reading.
type blockedCloser struct{ release chan struct{} }

func (b blockedCloser) Close() error { <-b.release; return nil }

func TestConnGroupCloseAllDoesNotWaitForeverOnAStuckConnection(t *testing.T) {
	old := closeWait
	closeWait = 100 * time.Millisecond
	t.Cleanup(func() { closeWait = old })

	g := NewConnGroup()
	stuck := blockedCloser{release: make(chan struct{})}
	t.Cleanup(func() { close(stuck.release) })
	g.Hold(stuck)
	fine := new(closeCounter)
	g.Hold(fine)

	start := time.Now()
	g.CloseAll()

	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("CloseAll took %v with a stuck connection, want about closeWait", d)
	}
	deadline := time.Now().Add(time.Second)
	for fine.n.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fine.n.Load() != 1 {
		t.Errorf("a healthy connection was closed %d times, want once", fine.n.Load())
	}
}
