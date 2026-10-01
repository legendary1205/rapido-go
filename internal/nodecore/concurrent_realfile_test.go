package nodecore

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/legendary1205/rapido-go/internal/nodecore/traffic"
)

// startFileServer serves exactly fileSize random bytes at /file, with real
// HTTP Range support (net/http's own FileServer-equivalent machinery via
// http.ServeContent) - the same mechanism a segmented downloader's parallel
// range requests rely on. Returns the listener's address and the exact
// byte count served per full request.
func startFileServer(t *testing.T, fileSize int) string {
	t.Helper()
	data := make([]byte, fileSize)
	rand.New(rand.NewSource(1)).Read(data)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen file server: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "file.bin", time.Time{}, &bytesReaderAt{data: data})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// bytesReaderAt is an io.ReadSeeker over a fixed byte slice - just enough
// for http.ServeContent to do real Range-request handling against it.
type bytesReaderAt struct {
	data []byte
	pos  int64
}

func (b *bytesReaderAt) Read(p []byte) (int, error) {
	if b.pos >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += int64(n)
	return n, nil
}

func (b *bytesReaderAt) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = b.pos + offset
	case io.SeekEnd:
		newPos = int64(len(b.data)) + offset
	}
	b.pos = newPos
	return newPos, nil
}

// dialVLESSTLS is dialVLESS plus a TLS handshake first - matching
// production's real "main" inbound, which (unlike buildTestOptions's plain
// VLESS used elsewhere in this package) requires security=tls. A concurrency
// bug specific to the TLS-wrapped path wouldn't show up in a test against
// the plain-VLESS config.
func dialVLESSTLS(t *testing.T, nodeAddr, sni, uuid string, destHost string, destPort int) net.Conn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", nodeAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial node: %v", err)
	}
	tlsConn := tls.Client(raw, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
	if err := tlsConn.Handshake(); err != nil {
		raw.Close()
		t.Fatalf("tls handshake: %v", err)
	}
	client, err := vless.NewClient(uuid, "", logger.NOP())
	if err != nil {
		tlsConn.Close()
		t.Fatalf("vless.NewClient: %v", err)
	}
	dest := M.Socksaddr{Addr: netip.MustParseAddr(destHost), Port: uint16(destPort)}
	conn, err := client.DialConn(tlsConn, dest)
	if err != nil {
		tlsConn.Close()
		t.Fatalf("vless DialConn: %v", err)
	}
	return conn
}

// httpGetRange issues one HTTP request over conn and returns both the
// response's real total byte count (headers included - the true ground
// truth to compare against traffic.Manager's own count, which counts every
// byte written to the connection, not just the body) and the body's own
// byte count on its own (what a download tool's "bytes downloaded" UI
// would show, which never includes protocol headers).
func httpGetRange(t *testing.T, conn net.Conn, host, path string, start, end int64) (total, body int64) {
	t.Helper()
	var req string
	if start < 0 {
		req = fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host)
	} else {
		req = fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nRange: bytes=%d-%d\r\nConnection: close\r\n\r\n", path, host, start, end)
	}
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write http request: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	var headerEnd = -1
	var all []byte
	for headerEnd < 0 {
		n, err := conn.Read(buf)
		if n > 0 {
			all = append(all, buf[:n]...)
		}
		if err != nil {
			t.Fatalf("read response: %v (got %d bytes so far)", err, len(all))
		}
		for i := 0; i+3 < len(all); i++ {
			if all[i] == '\r' && all[i+1] == '\n' && all[i+2] == '\r' && all[i+3] == '\n' {
				headerEnd = i + 4
				break
			}
		}
	}
	totalSoFar := int64(len(all))
	for {
		n, err := conn.Read(buf)
		totalSoFar += int64(n)
		if err != nil {
			break
		}
	}
	return totalSoFar, totalSoFar - int64(headerEnd)
}

// TestConcurrentVLESSTLSDownloadsAgainstARealFileServerAreNotDoubleCounted
// is the most production-faithful reproduction attempt yet: TLS-wrapped
// VLESS (matching the real "main" inbound's security=tls), a real HTTP file
// server with real Range-request support (matching a segmented downloader),
// and a byte-for-byte known file size to compare against.
func TestConcurrentVLESSTLSDownloadsAgainstARealFileServerAreNotDoubleCounted(t *testing.T) {
	const uuidStr = "8f8a4c1e-1e2a-4b8a-9b1a-00000000face"
	const fileSize = 5_000_000
	const conns = 8

	certPEM, keyPEM := selfSignedCert(t)
	port := freePort(t)
	listen := badoptionAddr("127.0.0.1")
	opts := option.Options{
		Inbounds: []option.Inbound{{
			Type: "vless", Tag: forkTag,
			Options: &option.VLESSInboundOptions{
				ListenOptions: option.ListenOptions{Listen: &listen, ListenPort: uint16(port)},
				Users:         []option.VLESSUser{{Name: "idmuser", UUID: uuidStr}},
				InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{
					Enabled: true, Certificate: badoption.Listable[string]{certPEM}, Key: badoption.Listable[string]{keyPEM},
				}},
			},
		}},
		Outbounds: []option.Outbound{{Type: "direct", Tag: "direct-out", Options: &option.DirectOutboundOptions{}}},
		Route:     &option.RouteOptions{Final: "direct-out"},
	}
	mgr := traffic.NewManager()
	node, err := New(context.Background(), opts, mgr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := node.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { node.Close() })

	fileAddr := startFileServer(t, fileSize)
	fileHost, filePortStr, err := net.SplitHostPort(fileAddr)
	if err != nil {
		t.Fatalf("split file addr: %v", err)
	}
	filePort, err := strconv.Atoi(filePortStr)
	if err != nil {
		t.Fatalf("parse file port: %v", err)
	}
	nodeAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	// Control: a single connection fetching the whole file once.
	t.Run("single", func(t *testing.T) {
		conn := dialVLESSTLS(t, nodeAddr, "127.0.0.1", uuidStr, fileHost, filePort)
		defer conn.Close()
		total, body := httpGetRange(t, conn, fileHost, "/file", -1, -1)
		// body is what a download tool's own UI would show - exactly the
		// file size, headers excluded.
		if body != fileSize {
			t.Errorf("single-connection download body = %d bytes, want exactly %d", body, fileSize)
		}
		// total (headers included) is a few hundred bytes more - real,
		// legitimate protocol overhead, not slack for a counting bug.
		if total < fileSize || total > fileSize+512 {
			t.Errorf("single-connection download total = %d bytes, want %d..%d (file size + plausible header overhead)", total, fileSize, fileSize+512)
		}
		usage := mgr.Drain()["idmuser"]
		// The actual regression check: usage.Down must equal the real,
		// independently-measured total byte count exactly - not the bare
		// file size (which ignores legitimate header overhead), and
		// absolutely not some multiple of either.
		if usage.Down != total {
			t.Errorf("usage.Down after single connection = %d, want %d (the exact real byte count) - %.4fx",
				usage.Down, total, float64(usage.Down)/float64(total))
		}
	})

	// The real reproduction attempt: N parallel connections, each range-
	// requesting its own 1/N slice of the same file concurrently - exactly
	// what IDM/a segmented downloader does.
	t.Run("multi", func(t *testing.T) {
		chunk := fileSize / conns
		var wg sync.WaitGroup
		totals := make([]int64, conns)
		bodies := make([]int64, conns)
		for i := 0; i < conns; i++ {
			start := int64(i * chunk)
			end := start + int64(chunk) - 1
			if i == conns-1 {
				end = fileSize - 1
			}
			wg.Add(1)
			go func(idx int, s, e int64) {
				defer wg.Done()
				conn := dialVLESSTLS(t, nodeAddr, "127.0.0.1", uuidStr, fileHost, filePort)
				defer conn.Close()
				totals[idx], bodies[idx] = httpGetRange(t, conn, fileHost, "/file", s, e)
			}(i, start, end)
		}
		wg.Wait()

		var total, body int64
		for i := range totals {
			total += totals[i]
			body += bodies[i]
		}
		// body is the sum of what a download tool's own UI would show for
		// each parallel chunk - exactly the file size, headers excluded,
		// regardless of how many connections split it up.
		if body != fileSize {
			t.Errorf("sum of %d concurrent range downloads' bodies = %d bytes, want exactly %d - a coverage bug, not a counting one", conns, body, fileSize)
		}
		// total (headers included, once per connection) is a bit more -
		// real, legitimate overhead that scales with connection count, not
		// slack for a counting bug.
		if total < fileSize || total > fileSize+512*int64(conns) {
			t.Errorf("sum of %d concurrent range downloads' totals = %d bytes, want %d..%d (file size + plausible header overhead)",
				conns, total, fileSize, fileSize+512*int64(conns))
		}

		usage := mgr.Drain()["idmuser"]
		// The actual regression check: usage.Down must equal the real,
		// independently-measured byte count (total) - not the bare file
		// size, and absolutely not some multiple of it. This is the precise
		// shape of the reported production symptom: usage looking ~2x (or
		// worse) the real transferred amount specifically under several
		// simultaneous connections for one user.
		if usage.Down != total {
			t.Errorf("usage.Down after %d concurrent connections = %d, want %d (the exact real byte count) - %.4fx",
				conns, usage.Down, total, float64(usage.Down)/float64(total))
		}
	})
}
