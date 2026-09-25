package nodelog

import (
	"strings"
	"testing"
)

// classifyRaw runs a line the way Capture does.
func classifyRaw(raw string) (kind string, benign bool) {
	level, text, ok := ParseLine(StripANSI(raw))
	if !ok {
		return "", false
	}
	return Classify(level, text)
}

func TestClassifyRealProductionLines(t *testing.T) {
	cases := []struct {
		line string
		kind string // "" = must be kept
	}{
		// The five shapes seen on a production node, plain and with the ANSI
		// colour codes sing-box wrote before they were switched off.
		{`ERROR[0123] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: EOF`, KindClientEOF},
		{`ERROR[0123] [1234567 1m0s] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: mux connection closed: read frame header: EOF`, KindMuxClosed},
		{`ERROR[0123] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: TLS handshake: EOF`, KindTLSHandshake},
		{`ERROR[0123] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: unknown UUID: 0c1ec6e8-1111-2222-3333-444455556666`, KindUnknownUUID},
		{`ERROR[0123] connection: open connection to 5.6.7.8:443 using outbound/direct[germany~wg]: dial tcp 5.6.7.8:443: i/o timeout`, KindDialTimeout + ":germany~wg"},
		{"\x1b[31mERROR\x1b[0m[0123] [\x1b[38;5;70m3647852086\x1b[0m 131ms] inbound/vless[vless-in]: process connection from 127.0.0.1:5898: EOF", KindClientEOF},
		{"\x1b[31mERROR\x1b[0m[0123] connection: open connection to 5.6.7.8:443 using outbound/direct[germany~wg]: dial tcp 5.6.7.8:443: i/o timeout", KindDialTimeout + ":germany~wg"},

		// Other TLS handshake failures.
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: TLS handshake: tls: first record does not look like a TLS handshake`, KindTLSHandshake},
		{`ERROR[0001] inbound/trojan[t#443]: process connection from [2001:db8::7]:5678: TLS handshake: read tcp 10.0.0.1:443->[2001:db8::7]:5678: read: connection reset by peer`, KindTLSHandshake},
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: TLS handshake: remote error: tls: bad certificate`, KindTLSHandshake},

		// Inbound endings.
		{`ERROR[0001] inbound/vmess[v]: process connection from 1.2.3.4:5678: read request: unexpected EOF`, KindClientEOF},
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: read tcp 10.0.0.1:20001->1.2.3.4:5678: read: connection reset by peer`, KindClientReset},
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: write tcp 10.0.0.1:20001->1.2.3.4:5678: write: broken pipe`, KindBrokenPipe},
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: read tcp 10.0.0.1:20001->1.2.3.4:5678: use of closed network connection`, KindClosedConn},
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: context canceled`, KindCanceled},
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: read tcp 10.0.0.1:20001->1.2.3.4:5678: i/o timeout`, KindClientTimeout},
		{`ERROR[0001] inbound/trojan[t]: process connection from 1.2.3.4:5678: unknown user: abc`, KindUnknownUUID},
		{`ERROR[0001] inbound/shadowsocks[s]: process connection from 1.2.3.4:5678: EOF`, KindClientEOF},
		{`WARN[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: EOF`, KindClientEOF},
		{`ERROR[0001] [9 2s] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: mux connection closed: read tcp 1.1.1.1:1->2.2.2.2:2: read: connection reset by peer`, KindMuxClosed},

		// Connection manager endings.
		{`ERROR[0001] [12 3s] connection: connection upload closed: raw-read tcp 10.0.0.1:20001->1.2.3.4:5678: read: connection reset by peer`, KindClientReset},
		{`ERROR[0001] [12 3s] connection: connection download closed: raw-write tcp 10.0.0.1:20001->1.2.3.4:5678: write: broken pipe`, KindBrokenPipe},

		// Outbound dial failures, per tag and per reason (Linux and Windows wording).
		{`ERROR[0001] connection: open connection to 5.6.7.8:443 using outbound/direct[de~direct]: dial tcp 5.6.7.8:443: connect: connection refused`, KindDialRefused + ":de~direct"},
		{`ERROR[0001] inbound/vless[main#20006]: process connection from 127.0.0.1:53992: flow mismatch: expected xtls-rprx-vision, but got none`, KindFlowMismatch},
		{`ERROR[0001] connection: open connection to shopfb.net:80 using outbound/direct[usa~wg]: lookup shopfb.net: (exchange6: NXDOMAIN | exchange4: NXDOMAIN)`, KindDNSFailure + ":usa~wg"},
		{`ERROR[0001] connection: open connection to nohost.example:443 using outbound/direct[direct-out]: lookup nohost.example: no such host`, KindDNSFailure + ":direct-out"},
		{`ERROR[0097] [1777181730 732ms] connection: listen packet connection using  using outbound/direct[usa~wg]: lookup www.textyahoo.com: (exchange6: NXDOMAIN | exchange4: NXDOMAIN)`, KindDNSFailure + ":usa~wg"},
		{`ERROR[0097] connection: listen packet connection using  using outbound/direct[usa~wg]: dial udp 8.8.8.8:53: i/o timeout`, KindDialTimeout + ":usa~wg"},
		{`ERROR[0001] connection: open connection to 127.0.0.1:5780 using outbound/direct[direct-out]: dial tcp 127.0.0.1:5780: connectex: No connection could be made because the target machine actively refused it.`, KindDialRefused + ":direct-out"},
		{`ERROR[0001] connection: open connection to [2001:db8::1]:443 using outbound/socks[up]: dial tcp [2001:db8::1]:443: connect: network is unreachable`, KindDialUnreachable + ":up"},
		{`ERROR[0001] connection: open connection to 5.6.7.8:443 using outbound/direct[x]: dial tcp 5.6.7.8:443: connect: no route to host`, KindDialUnreachable + ":x"},
		{`ERROR[0001] connection: open connection to 5.6.7.8:443 using outbound/direct: dial tcp 5.6.7.8:443: i/o timeout`, KindDialTimeout + ":direct"},

		// What a scanner or a wrong secret looks like on the other protocols
		// (verbatim from the real core).
		{`ERROR[0000] [181008882 0ms] inbound/vmess[main]: process connection from 127.0.0.1:2529: bad header`, KindBadHandshake},
		{`ERROR[0000] [3134567437 0ms] inbound/vmess[main]: process connection from 127.0.0.1:2530: bad request`, KindBadHandshake},
		{`ERROR[0000] [3375808994 0ms] inbound/trojan[main]: process connection from 127.0.0.1:2539: bad request size: fallback disabled`, KindBadHandshake},
		{`ERROR[0000] [3145759852 0ms] inbound/trojan[main]: process connection from 127.0.0.1:2540: bad request: fallback disabled`, KindBadHandshake},
		{`ERROR[0000] [3122380829 0ms] inbound/shadowsocks[main]: process connection from 127.0.0.1:2550: shadowsocks: serve TCP from 127.0.0.1:2550: chacha20poly1305: message authentication failed`, KindBadHandshake},
		{`ERROR[0000] [2502959680 0ms] inbound/shadowsocks[main]: process connection from 127.0.0.1:2560: shadowsocks: serve TCP from 127.0.0.1:2560: cipher: message authentication failed`, KindBadHandshake},

		// Timestamped format.
		{`-0700 2026-09-25 04:00:00 ERROR inbound/vless[main#20001]: process connection from 1.2.3.4:5678: EOF`, KindClientEOF},

		// What must NOT be dropped.
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: bad request: version 7 is not supported`, ""},
		{`ERROR[0001] inbound/vless[main#20001]: transport serve error: accept tcp 0.0.0.0:20001: too many open files`, ""},
		{`ERROR[0001] connection: open connection to 5.6.7.8:443 using outbound/vless[up]: read handshake: authentication failed`, ""},
		{`ERROR[0001] connection: open connection to 5.6.7.8:443 using outbound/direct[x]: dns: lookup failed`, ""},
		{`ERROR[0001] router: rule match failed`, ""},
		{`ERROR[0001] connection: open connection to shopfb.net:80 using outbound/direct[usa~wg]: lookup shopfb.net: i/o timeout`, ""}, // a resolver that does not answer is a fault, not a missing name
		{`ERROR[0001] inbound/vless[main#20006]: process connection from 1.2.3.4:5: flow mismatch`, KindFlowMismatch},
		{`FATAL[0001] start service: listen tcp 0.0.0.0:443: bind: address already in use`, ""},
		{`INFO[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: EOF`, ""}, // not an error line
		{`DEBUG[0001] connection: open connection to 5.6.7.8:443 using outbound/direct[x]: dial tcp 5.6.7.8:443: i/o timeout`, ""},
		{`ERROR[0001] inbound/vless[main#20001]: process connection from 1.2.3.4:5678`, ""}, // no cause at all
		{`ERROR[0001] some unrelated error that mentions EOF`, ""},
		{`panic: runtime error: index out of range`, ""},
	}
	for _, tc := range cases {
		kind, benign := classifyRaw(tc.line)
		switch {
		case tc.kind == "" && benign:
			t.Errorf("dropped a line that must be kept (%s): %s", kind, tc.line)
		case tc.kind != "" && (!benign || kind != tc.kind):
			t.Errorf("line classified as (%q, %v), want %q: %s", kind, benign, tc.kind, tc.line)
		}
	}
}

func TestParseLine(t *testing.T) {
	cases := []struct {
		line, level, text string
		ok                bool
	}{
		{"ERROR[0123] inbound/vless[x]: boom", LevelError, "inbound/vless[x]: boom", true},
		{"WARN[0001] x", LevelWarn, "x", true},
		{"INFO[0001] sing-box started (0.01s)", LevelInfo, "sing-box started (0.01s)", true},
		{"DEBUG[0001] d", LevelDebug, "d", true},
		{"TRACE[0001] t", LevelDebug, "t", true},
		{"FATAL[0001] f", LevelError, "f", true},
		{"PANIC[0001] p", LevelError, "p", true},
		{"-0700 2026-09-25 04:00:00 WARN something", LevelWarn, "something", true},
		{"ERROR[0007] [1234567 1m0s] inbound/vless[m#1]: x", LevelError, "[1234567 1m0s] inbound/vless[m#1]: x", true},
		{"goroutine 5 [running]:", "", "goroutine 5 [running]:", false},
		{"ERRORS[0001] nope", "", "ERRORS[0001] nope", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		level, text, ok := ParseLine(tc.line)
		if level != tc.level || text != tc.text || ok != tc.ok {
			t.Errorf("ParseLine(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.line, level, text, ok, tc.level, tc.text, tc.ok)
		}
	}
}

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"plain":                        "plain",
		"\x1b[31mERROR\x1b[0m[0000] x": "ERROR[0000] x",
		"[\x1b[38;5;209m2392609217\x1b[0m 0ms] connection": "[2392609217 0ms] connection",
		"\x1b[1;33mwarn\x1b[0m":                            "warn",
		"":                                                 "",
	}
	for in, want := range cases {
		if got := StripANSI(in); got != want {
			t.Errorf("StripANSI(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Contains(StripANSI("\x1b[31mx\x1b[0m"), "\x1b") {
		t.Error("escape left behind")
	}
}
