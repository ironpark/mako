// Package adbfake is an adb server for tests, with devices that answer a
// few requests as adb's own would: enough to see mako's lending of them.
package adbfake

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Server is an adb server listening on the loopback, with devices. A
// device's "tcp:" services echo what they are sent, and its reverses are
// tried once each: the server connects to the host's port, says "ping",
// and keeps the line it hears back (Reversed).
type Server struct {
	Addr string

	mu       sync.Mutex
	devices  []string // their serials, each attached and authorized
	got      []string
	killed   bool
	reversed chan string
}

// Start runs a server with devices for the test.
func Start(t *testing.T, devices ...string) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), devices: devices, reversed: make(chan string, 8)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

// SetDevices attaches devices in place of those the server had.
func (s *Server) SetDevices(devices ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices = devices
}

func (s *Server) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.devices)
}

// Reversed is the next line a reverse's host port answered "ping" with.
func (s *Server) Reversed() <-chan string { return s.reversed }

// Requests are the requests the server was sent, in order.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

// Killed reports whether a client asked the server to stop.
func (s *Server) Killed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killed
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	transport := ""
	for {
		req, err := read(conn)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.got = append(s.got, req)
		s.mu.Unlock()
		okay := func(msg string) { reply(conn, "OKAY", msg) }
		fail := func(msg string) { reply(conn, "FAIL", msg) }
		switch {
		case req == "host:version":
			okay("0029")
			return
		case req == "host:devices", req == "host:devices-l", req == "host:track-devices":
			var b strings.Builder
			for i, d := range s.list() {
				if req == "host:devices-l" {
					fmt.Fprintf(&b, "%-22s device product:p%d model:Model_%d transport_id:%d\n", d, i, i, i+1)
				} else {
					fmt.Fprintf(&b, "%s\tdevice\n", d)
				}
			}
			okay(b.String())
			if req == "host:track-devices" {
				_, _ = io.Copy(io.Discard, conn)
			}
			return
		case req == "host:kill":
			s.mu.Lock()
			s.killed = true
			s.mu.Unlock()
			io.WriteString(conn, "OKAY")
			return
		case req == "host:transport-any":
			d, ok := s.only(conn)
			if !ok {
				return
			}
			transport = d
			io.WriteString(conn, "OKAY")
		case strings.HasPrefix(req, "host:tport:serial:"):
			d := strings.TrimPrefix(req, "host:tport:serial:")
			if !s.has(d) {
				fail("device '" + d + "' not found")
				return
			}
			transport = d
			io.WriteString(conn, "OKAY\x01\x00\x00\x00\x00\x00\x00\x00")
		case transport != "" && strings.HasPrefix(req, "tcp:"):
			io.WriteString(conn, "OKAY")
			_, _ = io.Copy(conn, conn)
			return
		case transport != "" && strings.HasPrefix(req, "reverse:forward:"):
			dev, host, _ := strings.Cut(strings.TrimPrefix(req, "reverse:forward:"), ";")
			io.WriteString(conn, "OKAYOKAY")
			reply(conn, "", strings.TrimPrefix(dev, "tcp:"))
			go s.reverse(strings.TrimPrefix(host, "tcp:"))
			return
		case strings.HasPrefix(req, "host:transport:"):
			d := strings.TrimPrefix(req, "host:transport:")
			if !s.has(d) {
				fail("device '" + d + "' not found")
				return
			}
			transport = d
			io.WriteString(conn, "OKAY")
		case strings.HasPrefix(req, "host-serial:"):
			rest := strings.TrimPrefix(req, "host-serial:")
			for _, d := range s.list() {
				if cmd, ok := strings.CutPrefix(rest, d+":"); ok && cmd == "get-state" {
					okay("device")
					return
				}
			}
			fail("device not found")
			return
		case req == "host:get-state":
			if _, ok := s.only(conn); ok {
				okay("device")
			}
			return
		case strings.HasPrefix(req, "shell:"):
			if transport == "" {
				d, ok := s.only(conn)
				if !ok {
					return
				}
				transport = d
			}
			io.WriteString(conn, "OKAY")
			io.WriteString(conn, transport+":"+strings.TrimPrefix(req, "shell:"))
			return
		default:
			fail("unknown request " + req)
			return
		}
	}
}

func (s *Server) has(serial string) bool { return slices.Contains(s.list(), serial) }

// reverse connects to port on the loopback, as a device's reverse makes
// its server do, and keeps what it answers "ping" with.
func (s *Server) reverse(port string) {
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		s.reversed <- "dial: " + err.Error()
		return
	}
	defer c.Close()
	io.WriteString(c, "ping\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		line = "read: " + err.Error()
	}
	s.reversed <- line
}

// only is the server's one device, which a request for any device goes
// to; with more than one, it fails the request.
func (s *Server) only(conn net.Conn) (string, bool) {
	devices := s.list()
	if len(devices) != 1 {
		reply(conn, "FAIL", "more than one device/emulator")
		return "", false
	}
	return devices[0], true
}

func reply(w io.Writer, status, msg string) { fmt.Fprintf(w, "%s%04x%s", status, len(msg), msg) }

func read(r io.Reader) (string, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(string(head[:]), 16, 16)
	if err != nil {
		return "", err
	}
	buf := make([]byte, n)
	_, err = io.ReadFull(r, buf)
	return string(buf), err
}

// Request sends req to an adb server at addr, as an adb client would, and
// is what the server answered: "OKAY" and the rest, or "FAIL" and its
// message, until the server closed the connection.
func Request(addr string, reqs ...string) (string, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	for _, req := range reqs {
		if _, err := fmt.Fprintf(conn, "%04x%s", len(req), req); err != nil {
			return "", err
		}
	}
	out, err := io.ReadAll(conn)
	return string(out), err
}
