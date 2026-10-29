// Package adbfake is an adb server for tests, with devices that answer a
// few requests as adb's own would: enough to see mako's lending of them.
package adbfake

import (
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Server is an adb server listening on the loopback, with Devices.
type Server struct {
	Addr    string
	Devices []string // their serials, each attached and authorized

	mu     sync.Mutex
	got    []string
	killed bool
}

// Start runs a server with devices for the test.
func Start(t *testing.T, devices ...string) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), Devices: devices}
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
			for i, d := range s.Devices {
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
			for _, d := range s.Devices {
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

func (s *Server) has(serial string) bool { return slices.Contains(s.Devices, serial) }

// only is the server's one device, which a request for any device goes
// to; with more than one, it fails the request.
func (s *Server) only(conn net.Conn) (string, bool) {
	if len(s.Devices) != 1 {
		reply(conn, "FAIL", "more than one device/emulator")
		return "", false
	}
	return s.Devices[0], true
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
