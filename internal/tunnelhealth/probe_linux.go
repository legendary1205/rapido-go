//go:build linux

package tunnelhealth

import (
	"context"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Several independent well-known addresses: a tunnel counts as down only
// when none of them answers, so one unreachable target never condemns a
// working exit. Only the TCP handshake is needed - no data is sent.
var probeTargets = []string{"1.1.1.1:443", "8.8.8.8:443", "9.9.9.9:443"}

// DialProbe opens a TCP connection to one of probeTargets with the socket
// pinned to iface - the same SO_BINDTODEVICE binding sing-box applies for an
// outbound's bind_interface - so it exercises exactly the path user traffic
// takes. The first target to answer wins.
func DialProbe(ctx context.Context, iface string) (time.Duration, error) {
	if _, err := net.InterfaceByName(iface); err != nil {
		return 0, ErrMissing
	}
	dialer := net.Dialer{Control: func(network, address string, c syscall.RawConn) error {
		var bindErr error
		if err := c.Control(func(fd uintptr) { bindErr = unix.BindToDevice(int(fd), iface) }); err != nil {
			return err
		}
		return bindErr
	}}

	type result struct {
		rtt time.Duration
		err error
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, len(probeTargets))
	for _, target := range probeTargets {
		go func(target string) {
			start := time.Now()
			conn, err := dialer.DialContext(ctx, "tcp4", target)
			if err != nil {
				results <- result{err: err}
				return
			}
			conn.Close()
			results <- result{rtt: time.Since(start)}
		}(target)
	}
	var firstErr error
	for range probeTargets {
		r := <-results
		if r.err == nil {
			return r.rtt, nil
		}
		if firstErr == nil {
			firstErr = r.err
		}
	}
	return 0, firstErr
}
