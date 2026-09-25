package loadmap

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

const (
	// dnsTTL is how long a resolved name is trusted before a background
	// refresh; the stale answer keeps being used while that runs.
	dnsTTL = 5 * time.Minute
	// dnsFailTTL is how soon a name that failed to resolve is tried again.
	dnsFailTTL = 1 * time.Minute
	// dnsTimeout caps one lookup.
	dnsTimeout = 5 * time.Second
	// dnsParallel bounds the lookups in flight at once.
	dnsParallel = 8
)

// dnsCache resolves names in the background and only ever answers from
// memory: lookup returns at once with whatever is cached (nothing on the very
// first miss) and, when the entry is missing or expired, starts one refresh.
type dnsCache struct {
	resolver Resolver
	now      func() time.Time
	sem      chan struct{}
	wg       sync.WaitGroup // lets tests wait for in-flight lookups

	mu      sync.Mutex
	entries map[string]*dnsEntry
}

type dnsEntry struct {
	ips        map[string]struct{} // normalized; nil until a lookup succeeded
	at         time.Time           // when the last lookup finished
	failed     bool                // the last lookup failed
	refreshing bool
}

func newDNSCache(r Resolver, now func() time.Time) *dnsCache {
	return &dnsCache{
		resolver: r, now: now,
		sem:     make(chan struct{}, dnsParallel),
		entries: make(map[string]*dnsEntry),
	}
}

// lookup returns the IPs behind name. An IP literal answers itself without any
// resolver call. The returned map is shared and must not be modified.
func (c *dnsCache) lookup(name string) map[string]struct{} {
	if a, err := netip.ParseAddr(name); err == nil {
		return map[string]struct{}{a.Unmap().String(): {}}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[name]
	if e == nil {
		e = &dnsEntry{refreshing: true}
		c.entries[name] = e
		c.spawn(name)
		return nil
	}
	if !e.refreshing {
		ttl := dnsTTL
		if e.failed {
			ttl = dnsFailTTL
		}
		if c.now().Sub(e.at) >= ttl {
			e.refreshing = true
			c.spawn(name)
		}
	}
	return e.ips
}

// spawn starts one background lookup; the caller has set refreshing.
func (c *dnsCache) spawn(name string) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.sem <- struct{}{}
		defer func() { <-c.sem }()

		ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
		defer cancel()
		addrs, err := c.resolver.LookupHost(ctx, name)

		ips := make(map[string]struct{}, len(addrs))
		for _, s := range addrs {
			if a, perr := netip.ParseAddr(s); perr == nil {
				ips[a.Unmap().String()] = struct{}{}
			}
		}

		c.mu.Lock()
		defer c.mu.Unlock()
		e := c.entries[name]
		e.refreshing = false
		e.at = c.now()
		if err != nil || len(ips) == 0 {
			// Keep the previous answer, if any, rather than forgetting a
			// working mapping over one transient failure.
			e.failed = true
			return
		}
		e.failed = false
		e.ips = ips
	}()
}
