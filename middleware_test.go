package wakeonlan

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// fakeClock replaces the throttle clock for the duration of a test and
// resets the shared throttle state before and after it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func useFakeClock(t *testing.T) *fakeClock {
	t.Helper()
	clock := &fakeClock{t: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}
	reset := func() {
		throttleMu.Lock()
		lastSent = map[string]time.Time{}
		throttleMu.Unlock()
	}
	reset()
	now = clock.Now
	t.Cleanup(func() {
		now = time.Now
		reset()
	})
	return clock
}

// listen opens a local UDP socket that stands in for the broadcast address.
func listen(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// provision builds a Middleware that sends to the given listener.
func provision(t *testing.T, mac string, listener *net.UDPConn, throttle time.Duration) *Middleware {
	t.Helper()
	m := &Middleware{
		MAC:              mac,
		BroadcastAddress: listener.LocalAddr().String(),
		Throttle:         caddy.Duration(throttle),
	}
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	t.Cleanup(cancel)
	if err := m.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() { _ = m.Cleanup() })
	return m
}

// serve passes one request through the middleware. It only reports errors
// with t.Errorf, so it is safe to call from other goroutines.
func serve(t *testing.T, m *Middleware) {
	t.Helper()
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { return nil })
	r := httptest.NewRequest(http.MethodGet, "http://example.com/api/version", nil)
	if err := m.ServeHTTP(httptest.NewRecorder(), r, next); err != nil {
		t.Errorf("serve: %v", err)
	}
}

// received counts the packets waiting on the listener with a 100ms timeout,
// and checks that each of them is the expected magic packet.
func received(t *testing.T, conn *net.UDPConn, mac string) int {
	t.Helper()
	hw, _ := net.ParseMAC(mac)
	want := BuildMagicPacket(hw)
	buf := make([]byte, 1024)
	count := 0
	for {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := conn.Read(buf)
		if err != nil {
			return count
		}
		if string(buf[:n]) != string(want) {
			t.Errorf("unexpected packet: %x", buf[:n])
		}
		count++
	}
}

// receivedNone asserts that no packets arrive within 20ms, checking each
// against the expected magic packet.
func receivedNone(t *testing.T, conn *net.UDPConn, mac string) int {
	t.Helper()
	hw, _ := net.ParseMAC(mac)
	want := BuildMagicPacket(hw)
	buf := make([]byte, 1024)
	count := 0
	for {
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		n, err := conn.Read(buf)
		if err != nil {
			return count
		}
		if string(buf[:n]) != string(want) {
			t.Errorf("unexpected packet: %x", buf[:n])
		}
		count++
	}
}

func TestThrottle(t *testing.T) {
	const mac = "CC:C4:45:32:7A:51"

	t.Run("Window", func(t *testing.T) {
		clock := useFakeClock(t)
		conn := listen(t)
		m := provision(t, mac, conn, 30*time.Second)

		serve(t, m)
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("first request sent %d packets, want 1", n)
		}

		clock.Advance(29 * time.Second)
		serve(t, m)
		if n := receivedNone(t, conn, mac); n != 0 {
			t.Errorf("request within the window sent %d packets, want 0", n)
		}

		clock.Advance(1 * time.Second)
		serve(t, m)
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("request after the window sent %d packets, want 1", n)
		}
	})

	t.Run("Default", func(t *testing.T) {
		clock := useFakeClock(t)
		conn := listen(t)
		m := provision(t, mac, conn, 0)

		if got := time.Duration(m.Throttle); got != defaultThrottle {
			t.Errorf("default throttle is %s, want %s", got, defaultThrottle)
		}

		serve(t, m)
		clock.Advance(defaultThrottle - time.Second)
		serve(t, m)
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("sent %d packets within the default window, want 1", n)
		}

		clock.Advance(time.Second)
		serve(t, m)
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("sent %d packets at the end of the default window, want 1", n)
		}
	})

	t.Run("IndependentMACs", func(t *testing.T) {
		useFakeClock(t)
		const other = "00:11:22:33:44:55"
		conn := listen(t)
		first := provision(t, mac, conn, time.Minute)
		conn2 := listen(t)
		second := provision(t, other, conn2, time.Minute)

		serve(t, first)
		serve(t, second)
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("first MAC sent %d packets, want 1", n)
		}
		if n := received(t, conn2, other); n != 1 {
			t.Errorf("second MAC sent %d packets, want 1", n)
		}
	})

	t.Run("SurvivesReload", func(t *testing.T) {
		clock := useFakeClock(t)
		conn := listen(t)
		serve(t, provision(t, mac, conn, time.Minute))

		// A reload provisions a new handler for the same target.
		clock.Advance(30 * time.Second)
		serve(t, provision(t, mac, conn, time.Minute))
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("sent %d packets across a reload, want 1", n)
		}
	})

	t.Run("Concurrent", func(t *testing.T) {
		useFakeClock(t)
		conn := listen(t)
		m := provision(t, mac, conn, time.Minute)

		var wg sync.WaitGroup
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				serve(t, m)
			}()
		}
		wg.Wait()
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("concurrent requests sent %d packets, want 1", n)
		}
	})

	t.Run("WriteFailureIsNotRecorded", func(t *testing.T) {
		clock := useFakeClock(t)
		conn := listen(t)
		m := provision(t, mac, conn, time.Minute)

		// Break the socket, so the write fails.
		good := m.broadcastSocket
		broken, err := net.Dial("udp", conn.LocalAddr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = broken.Close()
		m.broadcastSocket = broken

		next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { return nil })
		r := httptest.NewRequest(http.MethodGet, "http://example.com/api/version", nil)
		if err := m.ServeHTTP(httptest.NewRecorder(), r, next); err == nil {
			t.Fatal("write to a closed socket did not fail")
		}
		throttleMu.Lock()
		_, recorded := lastSent[m.key]
		throttleMu.Unlock()
		if recorded {
			t.Error("failed write was recorded in the throttle state")
		}

		// The next request, after 1 second, still falls inside the throttle window and must send.
		m.broadcastSocket = good
		clock.Advance(time.Second)
		serve(t, m)
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("request after a failed write sent %d packets, want 1", n)
		}
	})

	t.Run("WriteFailureKeepsPreviousSend", func(t *testing.T) {
		clock := useFakeClock(t)
		conn := listen(t)
		m := provision(t, mac, conn, time.Minute)

		serve(t, m)
		if n := received(t, conn, mac); n != 1 {
			t.Fatalf("first request sent %d packets, want 1", n)
		}

		// After the window a send fails; the earlier record must survive
		// the rollback, so the window does not restart from the failure.
		good := m.broadcastSocket
		broken, err := net.Dial("udp", conn.LocalAddr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = broken.Close()
		m.broadcastSocket = broken
		clock.Advance(time.Minute)
		next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { return nil })
		r := httptest.NewRequest(http.MethodGet, "http://example.com/api/version", nil)
		if err := m.ServeHTTP(httptest.NewRecorder(), r, next); err == nil {
			t.Fatal("write to a closed socket did not fail")
		}

		m.broadcastSocket = good
		serve(t, m)
		if n := received(t, conn, mac); n != 1 {
			t.Errorf("request after a failed write sent %d packets, want 1", n)
		}
	})

	t.Run("RejectNegative", func(t *testing.T) {
		m := &Middleware{MAC: mac, BroadcastAddress: "127.0.0.1:9", Throttle: caddy.Duration(-time.Second)}
		ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
		defer cancel()
		if err := m.Provision(ctx); err == nil {
			t.Error("negative throttle was accepted")
		}
		// Caddy cleans up modules whose provisioning failed.
		if err := m.Cleanup(); err != nil {
			t.Errorf("cleanup after failed provision: %v", err)
		}
	})
}

func TestUnmarshalCaddyfile(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   bool
		broadcast string
		throttle  time.Duration
	}{
		{
			name:      "MACOnly",
			input:     `wake_on_lan 00:11:22:33:44:55`,
			broadcast: "255.255.255.255:9",
		},
		{
			name:      "Broadcast",
			input:     `wake_on_lan 00:11:22:33:44:55 192.168.0.255:7`,
			broadcast: "192.168.0.255:7",
		},
		{
			name: "Block",
			input: `wake_on_lan 00:11:22:33:44:55 {
				throttle 5s
			}`,
			broadcast: "255.255.255.255:9",
			throttle:  5 * time.Second,
		},
		{
			name: "BlockWithBroadcast",
			input: `wake_on_lan 00:11:22:33:44:55 192.168.0.255:7 {
				throttle 1m
			}`,
			broadcast: "192.168.0.255:7",
			throttle:  time.Minute,
		},
		{
			name:    "NoArguments",
			input:   `wake_on_lan`,
			wantErr: true,
		},
		{
			name:    "TooManyArguments",
			input:   `wake_on_lan 00:11:22:33:44:55 192.168.0.255:7 extra`,
			wantErr: true,
		},
		{
			name: "MissingDuration",
			input: `wake_on_lan 00:11:22:33:44:55 {
				throttle
			}`,
			wantErr: true,
		},
		{
			name: "InvalidDuration",
			input: `wake_on_lan 00:11:22:33:44:55 {
				throttle soon
			}`,
			wantErr: true,
		},
		{
			name: "ZeroDuration",
			input: `wake_on_lan 00:11:22:33:44:55 {
				throttle 0s
			}`,
			wantErr: true,
		},
		{
			name: "ExtraDurationArgument",
			input: `wake_on_lan 00:11:22:33:44:55 {
				throttle 5s 10s
			}`,
			wantErr: true,
		},
		{
			name: "UnknownSubdirective",
			input: `wake_on_lan 00:11:22:33:44:55 {
				retries 3
			}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m Middleware
			err := m.UnmarshalCaddyfile(caddyfile.NewTestDispenser(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected an error, got %+v", m)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.MAC != "00:11:22:33:44:55" {
				t.Errorf("incorrect MAC: %q", m.MAC)
			}
			if m.BroadcastAddress != tc.broadcast {
				t.Errorf("incorrect broadcast address: %q", m.BroadcastAddress)
			}
			if got := time.Duration(m.Throttle); got != tc.throttle {
				t.Errorf("incorrect throttle: %s", got)
			}
		})
	}
}
