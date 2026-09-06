package traffic

import (
	"sync"
	"testing"
)

func TestManagerAddAndDrain(t *testing.T) {
	m := NewManager()
	m.Add("alice", 100, 200)
	m.Add("alice", 50, 0)
	m.Add("bob", 10, 10)

	got := m.Drain()
	if got["alice"] != (Usage{Up: 150, Down: 200}) {
		t.Errorf("alice = %+v, want {150 200}", got["alice"])
	}
	if got["bob"] != (Usage{Up: 10, Down: 10}) {
		t.Errorf("bob = %+v, want {10 10}", got["bob"])
	}
	if len(got) != 2 {
		t.Errorf("len(got) = %d, want 2", len(got))
	}
}

func TestManagerDrainResets(t *testing.T) {
	m := NewManager()
	m.Add("alice", 100, 0)
	_ = m.Drain()

	second := m.Drain()
	if _, ok := second["alice"]; ok {
		t.Errorf("alice still present after a second Drain with no new Add - reset-on-read broken: %+v", second)
	}

	m.Add("alice", 5, 0)
	third := m.Drain()
	if third["alice"] != (Usage{Up: 5, Down: 0}) {
		t.Errorf("alice after re-adding post-drain = %+v, want {5 0} (not carrying over the earlier 100)", third["alice"])
	}
}

func TestManagerZeroAddIsANoOp(t *testing.T) {
	m := NewManager()
	m.Add("alice", 0, 0)
	got := m.Drain()
	if len(got) != 0 {
		t.Errorf("a zero Add should not create an entry at all, got %+v", got)
	}
}

func TestManagerConcurrentAdd(t *testing.T) {
	m := NewManager()
	const goroutines = 50
	const perGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				m.Add("shared", 1, 2)
			}
		}()
	}
	wg.Wait()

	got := m.Drain()
	wantUp := int64(goroutines * perGoroutine)
	wantDown := int64(goroutines * perGoroutine * 2)
	if got["shared"] != (Usage{Up: wantUp, Down: wantDown}) {
		t.Errorf("shared = %+v, want {%d %d}", got["shared"], wantUp, wantDown)
	}
}
