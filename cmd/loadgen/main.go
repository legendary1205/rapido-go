// Command loadgen is the stress-test tool behind "how many clients can a node
// carry?". It has two halves:
//
//	loadgen sink -listen :9000,:9001,:9002
//	    A TCP server that streams zeros at whatever rate a client asks for. Give
//	    it several ports when more than ~25000 connections will end up on it: a
//	    TCP source port can only be used once per destination address and port.
//	    Run it where the node's traffic should end up (a machine next to the
//	    node, so the sink is never the bottleneck).
//
//	loadgen gen -proxy 127.0.0.1:1080 -target SINKHOST:9000,SINKHOST:9001 -conns 5000 -kbps 20
//	    Opens -conns TCP connections THROUGH a proxy (SOCKS5, e.g. a local
//	    sing-box client whose outbound is the node under test), each pulling
//	    -kbps kbit/s from the sink, and prints once per -report how many are
//	    established, how many failed or were dropped, connect latency and the
//	    throughput. -ramp-to/-ramp-step/-ramp-every grow the population in steps
//	    so the moment things bend shows up; -storm N opens N connections as fast
//	    as it can and reports how long the node took to accept them (what happens
//	    to a node when everyone reconnects at once).
//
// It sends nothing but zeros, holds no credentials and touches only the address
// it is pointed at.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sink":
		runSink(os.Args[2:])
	case "gen":
		runGen(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: loadgen sink -listen :9000")
	fmt.Fprintln(os.Stderr, "       loadgen gen -proxy 127.0.0.1:1080 -target host:9000 -conns N -kbps R [-hold 60s] [-ramp-to N -ramp-step S -ramp-every 30s] [-storm N]")
}

// ---- sink ------------------------------------------------------------------

func runSink(args []string) {
	fs := flag.NewFlagSet("sink", flag.ExitOnError)
	listen := fs.String("listen", ":9000", "address(es) to listen on, comma separated")
	_ = fs.Parse(args)
	for _, addr := range strings.Split(*listen, ",") {
		ln, err := net.Listen("tcp", strings.TrimSpace(addr))
		if err != nil {
			fmt.Fprintln(os.Stderr, "listen:", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "sink listening on", ln.Addr())
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					continue
				}
				go serveSink(c)
			}
		}()
	}
	select {}
}

// serveSink reads one line "<bytes per second>\n" and then streams zeros at
// that rate until the client goes away.
func serveSink(c net.Conn) {
	defer c.Close()
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return
	}
	bps, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || bps <= 0 {
		return
	}
	// one chunk per tick keeps the timer count low even with tens of thousands
	// of clients: up to 4 ticks a second, but never so many that a chunk falls
	// below ~1.4 kB (a slow trickle is one packet a second, not four tiny ones).
	ticksPerSecond := min(max(bps/1400, 1), 4)
	chunk := make([]byte, bps/ticksPerSecond+1)
	tick := time.NewTicker(time.Second / time.Duration(ticksPerSecond))
	defer tick.Stop()
	for range tick.C {
		_ = c.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := c.Write(chunk); err != nil {
			return
		}
	}
}

// ---- generator ---------------------------------------------------------------

type stats struct {
	established atomic.Int64 // currently open
	opened      atomic.Int64 // ever opened
	failed      atomic.Int64 // could not be opened
	dropped     atomic.Int64 // died after opening
	bytes       atomic.Int64
	mu          sync.Mutex
	connectMS   []float64 // connect latencies since the last report
}

func (s *stats) recordConnect(d time.Duration) {
	s.mu.Lock()
	s.connectMS = append(s.connectMS, float64(d.Microseconds())/1000)
	s.mu.Unlock()
}

func (s *stats) drainConnect() (p50, p99 float64, n int) {
	s.mu.Lock()
	v := s.connectMS
	s.connectMS = nil
	s.mu.Unlock()
	if len(v) == 0 {
		return 0, 0, 0
	}
	sort.Float64s(v)
	return v[len(v)/2], v[int(float64(len(v))*0.99)], len(v)
}

func runGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	proxy := fs.String("proxy", "127.0.0.1:1080", "SOCKS5 proxy the traffic goes through, or a comma separated list (a TCP source port is used once per destination, so more than ~16k connections need several)")
	target := fs.String("target", "", "sink address host:port, or a comma separated list to spread connections over")
	conns := fs.Int("conns", 1000, "connections to hold")
	kbps := fs.Int("kbps", 20, "kbit/s each connection pulls")
	hold := fs.Duration("hold", 60*time.Second, "how long to hold the final population")
	rampTo := fs.Int("ramp-to", 0, "grow from -conns up to this many (0 = no ramp)")
	rampStep := fs.Int("ramp-step", 1000, "connections added per ramp step")
	rampEvery := fs.Duration("ramp-every", 30*time.Second, "time between ramp steps")
	openRate := fs.Int("open-rate", 500, "new connections per second while filling")
	storm := fs.Int("storm", 0, "open this many connections as fast as possible and report (a reconnect storm), then exit")
	report := fs.Duration("report", 5*time.Second, "report interval")
	stopOnDrop := fs.Float64("abort-drop-pct", 0, "stop early when more than this % of the population dropped or failed in one interval (0 = never)")
	_ = fs.Parse(args)
	if *target == "" {
		fmt.Fprintln(os.Stderr, "-target is required")
		os.Exit(2)
	}

	st := &stats{}
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() { <-sig; close(stop) }()

	targets := strings.Split(*target, ",")
	var next atomic.Uint64
	pick := func() string { return strings.TrimSpace(targets[next.Add(1)%uint64(len(targets))]) }
	proxies := strings.Split(*proxy, ",")
	var nextProxy atomic.Uint64
	pickProxy := func() string { return strings.TrimSpace(proxies[nextProxy.Add(1)%uint64(len(proxies))]) }
	bps := *kbps * 1000 / 8
	if bps < 1 {
		bps = 1
	}

	if *storm > 0 {
		runStorm(st, pickProxy, pick, *storm, bps, stop)
		return
	}

	var wg sync.WaitGroup
	open := func(n int) {
		// pace the opens so a big step is a controlled ramp, not a storm
		gap := time.Second / time.Duration(max(*openRate, 1))
		for i := 0; i < n; i++ {
			select {
			case <-stop:
				return
			default:
			}
			wg.Add(1)
			go func() { defer wg.Done(); worker(st, pickProxy(), pick(), bps, stop) }()
			time.Sleep(gap)
		}
	}

	start := time.Now()
	fmt.Println("t_s,target,established,opened,failed,dropped,connect_p50_ms,connect_p99_ms,mbit_s")
	population := 0
	go func() {
		population = *conns
		open(*conns)
		if *rampTo > *conns {
			for population < *rampTo {
				select {
				case <-stop:
					return
				case <-time.After(*rampEvery):
				}
				add := min(*rampStep, *rampTo-population)
				population += add
				open(add)
			}
		}
	}()

	lastBytes, lastFail, lastDrop := int64(0), int64(0), int64(0)
	end := time.Time{}
	tick := time.NewTicker(*report)
	defer tick.Stop()
loop:
	for {
		select {
		case <-stop:
			break loop
		case now := <-tick.C:
			b, f, d := st.bytes.Load(), st.failed.Load(), st.dropped.Load()
			p50, p99, _ := st.drainConnect()
			mbit := float64(b-lastBytes) * 8 / report.Seconds() / 1e6
			fmt.Printf("%.0f,%d,%d,%d,%d,%d,%.1f,%.1f,%.1f\n", now.Sub(start).Seconds(), population, st.established.Load(),
				st.opened.Load(), f, d, p50, p99, mbit)
			if *stopOnDrop > 0 && population > 0 {
				bad := float64((f-lastFail)+(d-lastDrop)) / float64(population) * 100
				if bad > *stopOnDrop {
					fmt.Printf("# aborting: %.1f%% of the population failed or dropped in one interval\n", bad)
					break loop
				}
			}
			lastBytes, lastFail, lastDrop = b, f, d
			// reached the final population? start the hold clock
			if end.IsZero() && st.established.Load() >= int64(max(*rampTo, *conns))*95/100 {
				end = now.Add(*hold)
			}
			if !end.IsZero() && now.After(end) {
				break loop
			}
		}
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
	wg.Wait()
}

func runStorm(st *stats, pickProxy, pick func() string, n, bps int, stop chan struct{}) {
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); worker(st, pickProxy(), pick(), bps, stop) }()
	}
	deadline := time.After(2 * time.Minute)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	fmt.Println("t_s,established,failed")
	for {
		select {
		case <-deadline:
			fmt.Printf("# storm: %d of %d established after 2 min\n", st.established.Load(), n)
			close(stop)
			wg.Wait()
			return
		case <-stop:
			wg.Wait()
			return
		case <-tick.C:
			e := st.established.Load()
			fmt.Printf("%.0f,%d,%d\n", time.Since(start).Seconds(), e, st.failed.Load())
			if e+st.failed.Load() >= int64(n) {
				p50, p99, _ := st.drainConnect()
				fmt.Printf("# storm: %d/%d established, %d failed, all answered in %.1f s; connect p50 %.0f ms p99 %.0f ms\n",
					e, n, st.failed.Load(), time.Since(start).Seconds(), p50, p99)
				close(stop)
				wg.Wait()
				return
			}
		}
	}
}

// worker opens one connection through the proxy, asks the sink for bps bytes a
// second and reads until stopped. It does not reconnect: a dropped connection is
// a data point, not something to hide.
func worker(st *stats, proxy, target string, bps int, stop chan struct{}) {
	t0 := time.Now()
	c, err := dialSocks(proxy, target)
	if err != nil {
		st.failed.Add(1)
		return
	}
	st.recordConnect(time.Since(t0))
	st.opened.Add(1)
	st.established.Add(1)
	defer func() { st.established.Add(-1); c.Close() }()
	if _, err := fmt.Fprintf(c, "%d\n", bps); err != nil {
		st.dropped.Add(1)
		return
	}
	go func() { <-stop; c.Close() }()
	buf := make([]byte, 32<<10)
	for {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := c.Read(buf)
		st.bytes.Add(int64(n))
		if err != nil {
			select {
			case <-stop:
			default:
				st.dropped.Add(1)
			}
			return
		}
	}
}

// dialSocks connects to the SOCKS5 proxy and asks it to CONNECT to target.
func dialSocks(proxy, target string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	c, err := net.DialTimeout("tcp", proxy, 10*time.Second)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	fail := func(e error) (net.Conn, error) { c.Close(); return nil, e }
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	var rep [2]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil || rep[0] != 5 || rep[1] != 0 {
		return fail(fmt.Errorf("socks: method refused"))
	}
	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 1)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 3, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		return fail(err)
	}
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil || head[1] != 0 {
		return fail(fmt.Errorf("socks: connect refused"))
	}
	skip := 0
	switch head[3] {
	case 1:
		skip = 4 + 2
	case 4:
		skip = 16 + 2
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return fail(err)
		}
		skip = int(l[0]) + 2
	}
	if _, err := io.CopyN(io.Discard, c, int64(skip)); err != nil {
		return fail(err)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}
