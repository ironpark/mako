package plugin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/mako/internal/plugin/hostapi"
	"github.com/ironpark/mako/internal/protocol"
)

// server is a test's stand-in for api.example.com, and evil, another
// host's; the fetcher sends api.example.com's requests to server.
func netRig(t *testing.T) (f *fetcher, server, evil *httptest.Server, hits *int) {
	t.Helper()
	hits = new(int)
	evil = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		io.WriteString(w, "evil")
	}))
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			io.WriteString(w, "ok from "+r.Host+" "+r.Header.Get("Authorization"))
		case "/away":
			http.Redirect(w, r, evil.URL+"/stolen", http.StatusFound)
		case "/away-named":
			http.Redirect(w, r, "https://evil.example.com/stolen", http.StatusFound)
		case "/here":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/big":
			w.Write([]byte(strings.Repeat("x", 2<<20)))
		case "/slow":
			time.Sleep(2 * time.Second)
		}
	}))
	t.Cleanup(evil.Close)
	t.Cleanup(server.Close)
	lim := DefaultLimits
	lim.HTTPTimeout = time.Second
	lim.HTTPPerMinute = 100
	f = newFetcher(lim, slog.New(slog.NewTextHandler(io.Discard, nil)), map[string]string{"api.example.com": server.URL})
	return f, server, evil, hits
}

func TestNetReachesOnlyGrantedHosts(t *testing.T) {
	f, _, _, hits := netRig(t)
	g := grants{"net:api.example.com": true}
	rate := newRateLimit(100, time.Minute)
	get := func(url string) (*hostapi.FetchResponse, error) {
		return f.fetch(context.Background(), "p", g, rate, &hostapi.FetchRequest{Url: url, Headers: map[string]string{"Authorization": "Bearer t"}})
	}
	resp, err := get("https://api.example.com/ok")
	if err != nil || resp.GetStatus() != 200 || resp.GetBody() != "ok from api.example.com Bearer t" {
		t.Fatalf("a granted request: %v %v", resp, err)
	}
	if resp, err := get("https://api.example.com/here"); err != nil || !strings.HasPrefix(resp.GetBody(), "ok") {
		t.Fatalf("a redirect within the host: %v %v", resp, err)
	}
	refused := map[string]string{
		"other host":            "https://evil.example.com/",
		"subdomain":             "https://x.api.example.com/",
		"suffix":                "https://api.example.com.evil.example/",
		"http":                  "http://api.example.com/ok",
		"port":                  "https://api.example.com:8443/ok",
		"userinfo":              "https://user:pass@api.example.com/ok",
		"userinfo trick":        "https://api.example.com@evil.example.com/ok",
		"IPv4":                  "https://127.0.0.1/ok",
		"IPv6":                  "https://[::1]/ok",
		"trailing dot":          "https://api.example.com./ok",
		"upper case":            "https://API.EXAMPLE.COM/ok",
		"encoded":               "https://api%2eexample.com/ok",
		"redirect away":         "https://api.example.com/away",
		"redirect to a name":    "https://api.example.com/away-named",
		"too large":             "https://api.example.com/big",
		"slow":                  "https://api.example.com/slow",
		"no host":               "https:///ok",
		"opaque":                "https:api.example.com/ok",
		"another scheme":        "file:///etc/passwd",
		"backslash host":        "https://api.example.com\\@evil.example.com/",
		"percent-encoded colon": "https://api.example.com%3A8443/ok",
	}
	for name, url := range refused {
		if resp, err := get(url); err == nil {
			t.Errorf("%s (%s) went through: %v", name, url, resp)
		}
	}
	if *hits > 0 {
		t.Fatalf("the other host was reached %d times", *hits)
	}
	// Headers the host sets itself, or that would change the connection.
	for _, h := range []string{"Host", "Connection", "Transfer-Encoding", "Proxy-Authorization", "X-Bad\r\nInjected"} {
		_, err := f.fetch(context.Background(), "p", g, rate, &hostapi.FetchRequest{Url: "https://api.example.com/ok", Headers: map[string]string{h: "x"}})
		if err == nil {
			t.Errorf("header %q was set", h)
		}
	}
	if _, err := f.fetch(context.Background(), "p", g, rate, &hostapi.FetchRequest{Url: "https://api.example.com/ok", Method: "CONNECT"}); err == nil {
		t.Error("CONNECT went through")
	}
}

func TestNetRateLimit(t *testing.T) {
	f, _, _, _ := netRig(t)
	g := grants{"net:api.example.com": true}
	rate := newRateLimit(3, time.Minute)
	for i := range 4 {
		_, err := f.fetch(context.Background(), "p", g, rate, &hostapi.FetchRequest{Url: "https://api.example.com/ok"})
		if (err != nil) != (i == 3) {
			t.Fatalf("request %d: %v", i, err)
		}
	}
}

// TestNetRefusesPrivateAddresses dials the user's own machine as a granted
// name resolving to it would: the dial guard refuses it.
func TestNetRefusesPrivateAddresses(t *testing.T) {
	_, server, _, _ := netRig(t)
	// A fetcher of no test's: the server is just an address on loopback.
	f := newFetcher(DefaultLimits, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	resp, err := f.client.Get(server.URL + "/ok")
	if err == nil {
		resp.Body.Close()
		t.Fatal("a loopback address was dialed")
	}
}

// Only the internet's addresses are public: none IANA's special-purpose
// registries do not mark global, and none of IPv6's forms of an IPv4
// address but for a public one.
func TestPublicAddresses(t *testing.T) {
	for _, c := range []struct {
		addr   string
		public bool
	}{
		{"8.8.8.8", true}, {"140.82.112.6", true}, {"2606:4700::1111", true}, {"2a00:1450:4001::200e", true},
		{"127.0.0.1", false}, {"10.1.2.3", false}, {"192.168.1.1", false}, {"172.16.0.1", false},
		{"169.254.169.254", false}, {"100.64.0.1", false}, {"0.0.0.0", false}, {"0.1.2.3", false},
		{"192.0.0.8", false}, {"192.0.2.1", false}, {"192.88.99.1", false}, {"198.18.0.1", false},
		{"198.51.100.1", false}, {"203.0.113.1", false}, {"224.0.0.1", false}, {"240.0.0.1", false},
		{"255.255.255.255", false},
		{"::", false}, {"::1", false}, {"fe80::1", false}, {"fe80::1%en0", false}, {"fd00::1", false},
		{"fd00:ec2::254", false}, {"fec0::1", false}, {"ff02::1", false}, {"100::1", false},
		{"100:0:0:1::1", false}, {"2001::1", false}, {"2001:0:4136:e378:8000:63bf:3fff:fdd2", false},
		{"2001:2::1", false}, {"2001:db8::1", false}, {"3fff::1", false}, {"5f00::1", false},
		{"64:ff9b:1::a00:1", false},
		// IPv4 in IPv6: as public as the IPv4 address it carries.
		{"::ffff:127.0.0.1", false}, {"::ffff:8.8.8.8", true},
		{"::127.0.0.1", false}, {"::10.0.0.1", false}, {"::8.8.8.8", true},
		{"::ffff:0:a00:1", false}, {"::ffff:0:808:808", true},
		{"64:ff9b::a00:1", false}, {"64:ff9b::a9fe:a9fe", false}, {"64:ff9b::808:808", true},
		{"2002:7f00:1::1", false}, {"2002:a9fe:a9fe::1", false}, {"2002:c0a8:101::1", false}, {"2002:808:808::1", true},
	} {
		if got := publicAddr(netip.MustParseAddr(c.addr)); got != c.public {
			t.Errorf("publicAddr(%s) = %v; want %v", c.addr, got, c.public)
		}
	}
}

// TestPluginFetchesThroughTheHost has a plugin's JavaScript make the
// requests, as granted and not.
func TestPluginFetchesThroughTheHost(t *testing.T) {
	_, server, evil, hits := netRig(t)
	r := newRig(t, testLimits, map[string]string{"api.example.com": server.URL, "evil.example.com": evil.URL})
	r.write("net", map[string]string{ManifestName: manifest("net", []string{"net:api.example.com"}, ""), "main.js": block(`
		const ok = await mako.http.fetch({ url: "https://api.example.com/ok" });
		s.msg = "got " + ok.status + " " + ok.body;
		try { await mako.http.fetch({ url: "https://evil.example.com/" }); s.msg += " evil reached" } catch (e) { s.msg += " | " + e.message }
		try { await mako.http.fetch({ url: "https://api.example.com/away" }); s.msg += " redirect followed" } catch (e) { s.msg += " | " + e.message }`,
		`ui.text(s.msg)`)})
	r.state("net", protocol.PluginWaiting)
	r.grant("net")
	w := r.watch("plugin:net/ui")
	tree := w.until("the requests", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "got") })
	text := strings.Join(texts(tree), " ")
	if !strings.Contains(text, "got 200 ok from api.example.com") || strings.Contains(text, "reached") || strings.Contains(text, "followed") ||
		!strings.Contains(text, "net:evil.example.com") || !strings.Contains(text, "another host") {
		t.Fatalf("%s", text)
	}
	if *hits > 0 {
		t.Fatal("the other host was reached")
	}
	if !strings.Contains(r.logs.String(), `msg="plugin net" plugin=net method=GET host=api.example.com path=/ok status=200`) {
		t.Fatalf("the request was not logged:\n%s", r.logs.String())
	}
}

// TestHostTimeIsNotThePlugins has a plugin wait on a response slower than
// its CPU time: the wait is the host's, and the plugin is not stopped.
func TestHostTimeIsNotThePlugins(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * runawayLimits.CPU)
		io.WriteString(w, "late")
	}))
	defer slow.Close()
	r := newRig(t, runawayLimits, map[string]string{"api.example.com": slow.URL})
	r.write("slow", map[string]string{ManifestName: manifest("slow", []string{"net:api.example.com"}, ""),
		"main.js": block(`s.msg = "got " + (await mako.http.fetch({ url: "https://api.example.com/" })).body`, `ui.text(s.msg)`)})
	r.state("slow", protocol.PluginWaiting)
	r.grant("slow")
	w := r.watch("plugin:slow/ui")
	w.until("the late answer", func(n *protocol.UiNode, _ *protocol.BlockUpdate) bool { return hasText(n, "got late") })
	r.state("slow", protocol.PluginRunning)
}
