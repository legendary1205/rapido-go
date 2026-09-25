// Package nodelog keeps the node's journal readable and lets the panel see the
// node's log live.
//
// The embedded sing-box core logs at ERROR level for ordinary client behaviour:
// a client that connects and hangs up, a scanner that speaks the wrong protocol,
// a destination that does not answer. One busy node produced dozens of such lines
// a second, which drowns everything that matters and loads journald. This package
// recognises those lines (Classify), drops them from the journal and reports them
// once a minute as a single aggregated line (Aggregator). Everything it does not
// recognise passes through untouched.
//
// The lines that are kept - and the agent's own JSON log - also go into a ring
// buffer (Ring) that the node's live channel serves to the panel on request.
package nodelog

import (
	"regexp"
	"strings"
)

// The kinds of benign noise. They are the keys of the "suppressed" counters the
// node reports, so they are a wire contract with the panel: add, never rename.
const (
	KindClientEOF     = "client_eof"       // a client hung up (or connected and said nothing)
	KindMuxClosed     = "mux_closed"       // a multiplexed session ended
	KindTLSHandshake  = "tls_handshake"    // a client failed the TLS handshake
	KindUnknownUUID   = "unknown_uuid"     // credentials that match no user
	KindBadHandshake  = "bad_handshake"    // a scanner's garbage, or a wrong secret, on a protocol that cannot say "unknown user"
	KindClientReset   = "client_reset"     // connection reset by the client
	KindBrokenPipe    = "broken_pipe"      // writing to a client that is gone
	KindClosedConn    = "closed_conn"      // use of a connection that was closed
	KindCanceled      = "context_canceled" // work abandoned because the connection went away
	KindClientTimeout = "client_timeout"   // i/o timeout on an established connection
	KindFlowMismatch  = "flow_mismatch"    // a VLESS client whose flow setting does not match the inbound's

	// Outbound dial failures carry the outbound's tag after a colon
	// ("dial_timeout:germany~wg"): they say which exit is unhealthy.
	KindDialTimeout     = "dial_timeout"
	KindDialRefused     = "dial_refused"
	KindDialUnreachable = "dial_unreachable"

	// A destination name that does not resolve ("dns_failure:usa~wg"): what the
	// client asked for does not exist, which says nothing about the exit.
	KindDNSFailure = "dns_failure"
)

// Levels are the four the panel knows.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// StripANSI removes terminal colour codes. sing-box is told not to emit them,
// but a line that reaches the pipe from anywhere else may still carry some.
func StripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiEscape.ReplaceAllString(s, "")
}

// sing-box prefixes a line with its level and either the seconds since start
// ("ERROR[0123] ") or, with timestamps on, "-0700 2006-01-02 15:04:05 ERROR ".
var linePrefix = regexp.MustCompile(`^(?:[+-]\d{4} \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} )?(TRACE|DEBUG|INFO|WARN|ERROR|FATAL|PANIC)(?:\[\d+\])? `)

// ParseLine splits one sing-box log line (colour codes already stripped, no
// trailing newline) into its level and the rest of the line. ok is false for a
// line that does not have a sing-box prefix; text is then the whole line.
func ParseLine(line string) (level, text string, ok bool) {
	m := linePrefix.FindStringSubmatchIndex(line)
	if m == nil {
		return "", line, false
	}
	switch line[m[2]:m[3]] {
	case "TRACE", "DEBUG":
		level = LevelDebug
	case "INFO":
		level = LevelInfo
	case "WARN":
		level = LevelWarn
	default:
		level = LevelError
	}
	return level, line[m[1]:], true
}

// Classify reports whether a parsed log line is benign client noise, and which
// kind. Only error and warning lines are ever candidates.
//
// The patterns are the wording of sing-box's own messages, for example
//
//	inbound/vless[main#20001]: process connection from 1.2.3.4:5678: EOF
//	[1234567 1m0s] inbound/vless[main#20001]: process connection from 1.2.3.4:5678: mux connection closed: read frame header: EOF
//	inbound/vless[main#20001]: process connection from 1.2.3.4:5678: TLS handshake: EOF
//	inbound/vless[main#20001]: process connection from 1.2.3.4:5678: unknown UUID: 0c1ec6e8-...
//	connection: open connection to 5.6.7.8:443 using outbound/direct[germany~wg]: dial tcp 5.6.7.8:443: i/o timeout
//
// Anything else - including an inbound error this list does not know - is not
// benign and is kept.
func Classify(level, text string) (kind string, benign bool) {
	if level != LevelError && level != LevelWarn {
		return "", false
	}
	if i := strings.Index(text, "open connection to "); i >= 0 {
		return classifyDial(text[i:])
	}
	// The same failure on a UDP association: "listen packet connection using  using outbound/...".
	if i := strings.Index(text, "listen packet connection using"); i >= 0 {
		return classifyDial(text[i:])
	}
	if i := strings.Index(text, "process connection from "); i >= 0 {
		return classifyInbound(text[i+len("process connection from "):])
	}
	for _, marker := range [...]string{"connection upload closed: ", "connection download closed: "} {
		if i := strings.Index(text, marker); i >= 0 {
			return benignTail(text[i+len(marker):])
		}
	}
	return "", false
}

// classifyInbound handles what follows "process connection from ": the client's
// address, then the cause.
func classifyInbound(rest string) (string, bool) {
	// The address ends at the first ": " - an IPv6 address is bracketed, so its
	// colons are never followed by a space.
	sep := strings.Index(rest, ": ")
	if sep < 0 {
		return "", false
	}
	cause := rest[sep+2:]
	switch {
	case strings.HasPrefix(cause, "TLS handshake"):
		// Every way a client can fail the handshake: probes, scanners, clients
		// that give up half way.
		return KindTLSHandshake, true
	case strings.Contains(cause, "unknown UUID"), strings.Contains(cause, "unknown user"):
		return KindUnknownUUID, true
	case isBadHandshake(cause):
		return KindBadHandshake, true
	case strings.HasPrefix(cause, "flow mismatch"):
		// "flow mismatch: expected xtls-rprx-vision, but got none": a client set up
		// without the flow its subscription carries.
		return KindFlowMismatch, true
	case strings.Contains(cause, "mux connection closed"):
		if _, ok := benignTail(cause); ok {
			return KindMuxClosed, true
		}
		return "", false
	}
	return benignTail(cause)
}

// isBadHandshake recognises how the other protocols refuse a connection whose
// first bytes are not a valid request from any of their users - a port scanner,
// a browser, a client with the wrong secret. Exact wording, as observed from the
// real core: a request that is invalid for any other reason is a fault to read.
func isBadHandshake(cause string) bool {
	switch {
	case cause == "bad header", cause == "bad request": // vmess
		return true
	case strings.HasSuffix(cause, ": fallback disabled"): // trojan: "bad request size: fallback disabled"
		return true
	case strings.Contains(cause, "message authentication failed"): // shadowsocks AEAD
		return true
	}
	return false
}

// benignTail recognises the ways a healthy connection ends badly: the peer left.
func benignTail(cause string) (string, bool) {
	cause = strings.TrimSpace(cause)
	switch {
	case cause == "EOF", strings.HasSuffix(cause, ": EOF"), strings.HasSuffix(cause, "unexpected EOF"):
		return KindClientEOF, true
	case strings.Contains(cause, "connection reset by peer"), strings.Contains(cause, "forcibly closed by the remote host"):
		return KindClientReset, true
	case strings.Contains(cause, "broken pipe"):
		return KindBrokenPipe, true
	case strings.Contains(cause, "use of closed network connection"):
		return KindClosedConn, true
	case strings.Contains(cause, "context canceled"):
		return KindCanceled, true
	case strings.Contains(cause, "i/o timeout"), strings.Contains(cause, "connection timed out"):
		return KindClientTimeout, true
	}
	return "", false
}

// "open connection to 5.6.7.8:443 using outbound/direct[germany~wg]: dial tcp ..."
// - the tag is the bracketed part, or the type when the outbound has none.
var dialLine = regexp.MustCompile(`^(?:open connection to \S+|listen packet connection using)\s+using outbound/([^\[\s:]+)(?:\[([^\]]+)\])?: (.*)$`)

func classifyDial(text string) (string, bool) {
	m := dialLine.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	tag, cause := m[2], m[3]
	if tag == "" {
		tag = m[1]
	}
	// A destination that does not exist. A resolver that does not answer at all
	// (a timeout) is a fault worth reading and stays out of this.
	if strings.HasPrefix(cause, "lookup ") && isNameNotFound(cause) {
		return KindDNSFailure + ":" + tag, true
	}
	// Only the dial itself: a failure further into the handshake with a proxy
	// (bad credentials, a protocol error) is a fault worth reading.
	if !strings.Contains(cause, "dial ") {
		return "", false
	}
	switch {
	case strings.Contains(cause, "i/o timeout"), strings.Contains(cause, "connection timed out"):
		return KindDialTimeout + ":" + tag, true
	case strings.Contains(cause, "connection refused"), strings.Contains(cause, "actively refused"):
		return KindDialRefused + ":" + tag, true
	case strings.Contains(cause, "unreachable"), strings.Contains(cause, "no route to host"):
		return KindDialUnreachable + ":" + tag, true
	}
	return "", false
}

// isNameNotFound recognises a resolver's answer that the name does not exist.
func isNameNotFound(cause string) bool {
	return strings.Contains(cause, "NXDOMAIN") || strings.Contains(cause, "no such host") || strings.Contains(cause, "server misbehaving")
}
