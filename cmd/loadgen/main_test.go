package main

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// fakeSocks is the smallest SOCKS5 server that satisfies dialSocks: no auth,
// CONNECT only, then it pipes to the target.
func fakeSocks(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var greet [3]byte
				if _, err := io.ReadFull(c, greet[:]); err != nil {
					return
				}
				c.Write([]byte{5, 0})
				var head [4]byte
				if _, err := io.ReadFull(c, head[:]); err != nil {
					return
				}
				var host string
				switch head[3] {
				case 1:
					var ip [4]byte
					io.ReadFull(c, ip[:])
					host = net.IP(ip[:]).String()
				case 3:
					var l [1]byte
					io.ReadFull(c, l[:])
					b := make([]byte, l[0])
					io.ReadFull(c, b)
					host = string(b)
				}
				var p [2]byte
				io.ReadFull(c, p[:])
				up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p[:])))))
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String()
}

func startSink(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSink(c)
		}
	}()
	return ln.Addr().String()
}

func TestWorkerPullsTheRequestedRateThroughSocks(t *testing.T) {
	proxy, sink := fakeSocks(t), startSink(t)
	st := &stats{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { worker(st, proxy, sink, 100_000, stop); close(done) }()

	time.Sleep(2200 * time.Millisecond)
	if got := st.established.Load(); got != 1 {
		t.Fatalf("established = %d, want 1", got)
	}
	close(stop)
	<-done
	// 100 kB/s for ~2 s, in quarter-second chunks: allow generous slack either way
	if b := st.bytes.Load(); b < 100_000 || b > 400_000 {
		t.Errorf("bytes = %d, want roughly 200000", b)
	}
	if st.failed.Load() != 0 || st.dropped.Load() != 0 {
		t.Errorf("failed=%d dropped=%d, want none", st.failed.Load(), st.dropped.Load())
	}
	if _, _, n := st.drainConnect(); n != 1 {
		t.Errorf("connect samples = %d, want 1", n)
	}
}

func TestWorkerCountsAFailedConnect(t *testing.T) {
	st := &stats{}
	stop := make(chan struct{})
	worker(st, "127.0.0.1:1", "127.0.0.1:2", 1000, stop) // nothing listens on port 1
	if st.failed.Load() != 1 || st.established.Load() != 0 {
		t.Errorf("failed=%d established=%d, want 1 and 0", st.failed.Load(), st.established.Load())
	}
}

func TestStormReportsWhenEveryoneIsAnswered(t *testing.T) {
	proxy, sink := fakeSocks(t), startSink(t)
	st := &stats{}
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		runStorm(st, func() string { return proxy }, func() string { return sink }, 50, 10_000, stop)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("storm never finished")
	}
	if st.opened.Load() != 50 {
		t.Errorf("opened = %d, want 50", st.opened.Load())
	}
}
