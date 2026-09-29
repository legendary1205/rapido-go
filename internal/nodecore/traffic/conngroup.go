package traffic

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	N "github.com/sagernet/sing/common/network"
)

// ConnGroup is every client connection one core generation has accepted and
// not yet finished with, so that closing the core can end them all.
//
// sing-box closes a core by stopping its listeners and its router, and the
// router's connection manager then drops the connections it is relaying. A
// multiplexed session is not one of those: its carrier connection is read by the
// inbound's own goroutine, which keeps accepting new streams from the client and
// routing each one into the closed router, where it fails ("dns router closed").
// The client sees a healthy connection that answers every request with an
// error, and nothing makes it reconnect. Closing the carrier connection is what
// ends the session, and only the inbound knows it - hence this group, filled by
// the inbounds and emptied by Node.Close.
//
// One group belongs to exactly one core generation (see nodecore.New), unlike
// the Manager, which lives as long as the process. A connection whose
// handshake finishes after its core was closed is therefore refused at once
// instead of joining the next generation.
//
// A nil *ConnGroup is valid and tracks nothing.
type ConnGroup struct {
	mu     sync.Mutex
	conns  map[uint64]io.Closer
	next   uint64
	closed bool
}

func NewConnGroup() *ConnGroup {
	return &ConnGroup{conns: make(map[uint64]io.Closer)}
}

type connGroupKey struct{}

// closeWorkers bounds how many connections CloseAll closes at once, and
// closeWait how long it waits for them: closing a TLS connection may block on a
// peer that has stopped reading, and neither one such peer nor many may hold up
// a core restart. Closes still running when the wait ends carry on in the
// background.
const closeWorkers = 64

var closeWait = 3 * time.Second

// Hold registers c and returns the func that forgets it again. The func is
// idempotent. If the group is already closed, c is closed instead and the func
// does nothing.
func (g *ConnGroup) Hold(c io.Closer) (release func()) {
	if g == nil || c == nil {
		return func() {}
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		c.Close()
		return func() {}
	}
	g.next++
	id := g.next
	g.conns[id] = c
	g.mu.Unlock()

	var released atomic.Bool
	return func() {
		if !released.CompareAndSwap(false, true) {
			return
		}
		g.mu.Lock()
		delete(g.conns, id)
		g.mu.Unlock()
	}
}

// Track is Hold for an inbound handler: it returns onClose with the forgetting
// chained in front of it. Hand the result to the router in place of onClose.
// onClose may be nil.
func (g *ConnGroup) Track(c io.Closer, onClose N.CloseHandlerFunc) N.CloseHandlerFunc {
	if g == nil {
		return onClose
	}
	release := g.Hold(c)
	return func(err error) {
		release()
		if onClose != nil {
			onClose(err)
		}
	}
}

// Len is how many connections the group holds right now.
func (g *ConnGroup) Len() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.conns)
}

// CloseAll closes every connection the group holds and refuses any later one.
// It returns once they are all closed, or after closeWait if some are stuck.
// Calling it again is harmless.
func (g *ConnGroup) CloseAll() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.closed = true
	all := make([]io.Closer, 0, len(g.conns))
	for _, c := range g.conns {
		all = append(all, c)
	}
	clear(g.conns)
	g.mu.Unlock()

	work := make(chan io.Closer)
	var wg sync.WaitGroup
	for range min(closeWorkers, len(all)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				c.Close()
			}
		}()
	}
	go func() {
		for _, c := range all {
			work <- c
		}
		close(work)
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
	}
}

// WithConnGroup and ConnGroupFromContext thread a core generation's group to
// its inbounds the same way NewContext does the Manager.
func WithConnGroup(ctx context.Context, g *ConnGroup) context.Context {
	return context.WithValue(ctx, connGroupKey{}, g)
}

func ConnGroupFromContext(ctx context.Context) *ConnGroup {
	g, _ := ctx.Value(connGroupKey{}).(*ConnGroup)
	return g
}
