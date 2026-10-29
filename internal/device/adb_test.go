package device

import (
	"context"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/ironpark/mako/internal/device/adbfake"
)

// Every request a client may make is sent for the device lent, or
// refused.
func TestARequestGoesToTheDeviceLentAlone(t *testing.T) {
	tid := func() (string, error) { return "3", nil }
	for _, c := range []struct {
		req, want string
		refused   bool
		first     bool
	}{
		{req: "host:version", want: "host:version"},
		{req: "host:kill", refused: true},
		{req: "host:killforward-all", refused: true},
		{req: "host:transport-any", want: "host:transport:S1"},
		{req: "host:transport-usb", want: "host:transport:S1"},
		{req: "host:tport:any", want: "host:tport:serial:S1"},
		{req: "host:transport:S1", want: "host:transport:S1"},
		{req: "host:transport:S2", refused: true},
		{req: "host:tport:serial:S2", refused: true},
		{req: "host:transport-id:3", want: "host:transport-id:3"},
		{req: "host:transport-id:4", refused: true},
		{req: "host-serial:S1:get-state", want: "host-serial:S1:get-state"},
		{req: "host-serial:S10:get-state", refused: true},
		{req: "host-serial:S2:forward:tcp:1;tcp:2", refused: true},
		{req: "host-usb:get-serialno", want: "host-serial:S1:get-serialno"},
		{req: "host-transport-id:3:features", want: "host-transport-id:3:features"},
		{req: "host-transport-id:9:features", refused: true},
		{req: "host:get-state", want: "host-serial:S1:get-state"},
		{req: "host:forward:tcp:8000;tcp:8000", want: "host-serial:S1:forward:tcp:8000;tcp:8000"},
		{req: "host:wait-for-any-device", want: "host-serial:S1:wait-for-any-device"},
		{req: "host:host-features", want: "host:host-features"},
		{req: "host:track-devices-proto-text", refused: true},
		{req: "shell:ls", want: "shell:ls", first: true},
	} {
		p, err := plan(c.req, "S1", tid)
		if c.refused {
			if err == nil {
				t.Errorf("%s: sent as %q", c.req, p.req)
			}
			continue
		}
		if err != nil || p.req != c.want || p.selectFirst != c.first {
			t.Errorf("%s: %+v, %v; want %q", c.req, p, err, c.want)
		}
	}
}

func TestListingsKeepTheDeviceLent(t *testing.T) {
	list := "S1\tdevice\nS10\tdevice\nemulator-5554          device product:x transport_id:2\n"
	if got := keepLines("S1")(list); got != "S1\tdevice\n" {
		t.Errorf("text: %q", got)
	}
	device := func(serial string) []byte {
		var d []byte
		d = protowire.AppendTag(d, 1, protowire.BytesType)
		d = protowire.AppendString(d, serial)
		d = protowire.AppendTag(d, 2, protowire.VarintType)
		return protowire.AppendVarint(d, 2)
	}
	var msg []byte
	for _, s := range []string{"S0", "S1", "S2"} {
		msg = protowire.AppendTag(msg, 1, protowire.BytesType)
		msg = protowire.AppendBytes(msg, device(s))
	}
	got := keepProto("S1")(string(msg))
	if len(got) == 0 || protoSerial([]byte(got)[2:]) != "S1" || strings.Contains(got, "S0") || strings.Contains(got, "S2") {
		t.Errorf("binary: %q", got)
	}
	if keepProto("S1")("\xff\xff") != "" {
		t.Error("a listing that does not parse kept something")
	}
}

// A client of the proxy sees one device of the server's two, reaches it
// as any device, and does not stop the server.
func TestAClientSeesTheDeviceLentAlone(t *testing.T) {
	srv := adbfake.Start(t, "S0", "S1")
	a := &ADB{Server: srv.Addr}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := a.List(ctx)
	if err != nil || len(list) != 2 || list[1].GetSerial() != "S1" || list[1].GetName() != "Model 1" || list[1].GetAttrs()["adb_version"] != "41" {
		t.Fatalf("listed %v, %v", list, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go a.Serve(ctx, "S1", conn)
		}
	}()
	addr := ln.Addr().String()
	for _, c := range []struct {
		reqs []string
		want string
	}{
		{[]string{"host:devices"}, "OKAY000aS1\tdevice\n"},
		{[]string{"host:transport-any", "shell:echo"}, "OKAYOKAYS1:echo"},
		{[]string{"shell:id"}, "OKAYS1:id"}, // the proxy chose the transport, and took its OKAY,
		{[]string{"host:get-state"}, "OKAY0006device"},
		{[]string{"host:transport:S0", "shell:id"}, "FAIL"},
		{[]string{"host:kill"}, "FAIL"},
	} {
		got, err := adbfake.Request(addr, c.reqs...)
		if c.want == "FAIL" {
			err = nil // refused, and closed before the rest of what was sent was read
		}
		if err != nil || !strings.HasPrefix(got, c.want) {
			t.Errorf("%v: %q, %v; want %q", c.reqs, got, err, c.want)
		}
	}
	// A listing kept up to date is filtered as it comes, until the client
	// goes.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeMessage(conn, "host:track-devices"); err != nil {
		t.Fatal(err)
	}
	if err := readStatus(conn); err != nil {
		t.Fatal(err)
	}
	if msg, err := readMessage(conn); err != nil || msg != "S1\tdevice\n" {
		t.Errorf("track-devices: %q, %v", msg, err)
	}

	if srv.Killed() {
		t.Error("the server was stopped")
	}
	if slices.Contains(srv.Requests(), "host:transport:S0") {
		t.Error("a request for another device reached the server")
	}
}
