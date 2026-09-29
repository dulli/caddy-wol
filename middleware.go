package wakeonlan

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// defaultThrottle is used when no throttle is configured.
const defaultThrottle = 10 * time.Minute

// The throttle state lives at package level, rather than in the Middleware,
// so that it survives config reloads: a reload provisions fresh handlers,
// but must not reset the time since the last packet.
var (
	throttleMu sync.Mutex
	// lastSent maps a MAC and broadcast address pair to the time the last
	// magic packet was sent to it.
	lastSent = map[string]time.Time{}
	// now is the clock used by the throttle, replaceable in tests.
	now = time.Now
)

func init() {
	caddy.RegisterModule(Middleware{})
	httpcaddyfile.RegisterHandlerDirective("wake_on_lan", parseCaddyfile)
}

// Middleware wakes up a target host on HTTP requests using wake-on-lan.
type Middleware struct {
	// MAC address of the target host in a net.ParseMAC compatible format.
	MAC string `json:"mac,omitempty"`
	// Broadcast address (<ip>:<port>) the magic packet should be sent to.
	// Defaults to "255.255.255.255:9".
	BroadcastAddress string `json:"broadcast_address,omitempty"`
	// Minimum time between two magic packets for the same MAC and broadcast
	// address. Defaults to 10 minutes.
	Throttle caddy.Duration `json:"throttle,omitempty"`

	key             string
	logger          *zap.Logger
	magicPacket     []byte
	broadcastSocket net.Conn
}

// CaddyModule returns the Caddy module information.
func (Middleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.wake_on_lan",
		New: func() caddy.Module { return new(Middleware) },
	}
}

// Prepare the magic packet and the socket used to send it
// Provision implements caddy.Provisioner.
func (m *Middleware) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger(m)

	if m.Throttle == 0 {
		m.Throttle = caddy.Duration(defaultThrottle)
	}
	if m.Throttle < 0 {
		return fmt.Errorf("throttle must be positive, got %s", time.Duration(m.Throttle))
	}

	mac, err := net.ParseMAC(m.MAC)
	if err != nil {
		return err
	}
	// Key on the parsed MAC so that different spellings of the same address
	// share one throttle.
	m.key = fmt.Sprintf("wol-%s-%s", mac, m.BroadcastAddress)
	m.magicPacket = BuildMagicPacket(mac)
	if err != nil {
		return err
	}
	m.broadcastSocket, err = net.Dial("udp", m.BroadcastAddress)
	if err != nil {
		return err
	}
	return nil
}

// ServeHTTP dispatches the prepared magic packet and transparently
// continues with the next http handler.
func (m Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if err := m.wake(r); err != nil {
		return err
	}
	return next.ServeHTTP(w, r)
}

// wake sends the magic packet unless one was sent to the same MAC and
// broadcast address within the throttle window. The decision, the record and
// the send happen under one lock, so concurrent requests cannot both send.
// Logging happens after the lock is released, so a slow log sink cannot hold
// up requests for other targets.
func (m Middleware) wake(r *http.Request) error {
	remaining, err := m.send()
	switch {
	case err != nil:
		m.logger.Error("failed to dispatch magic packet",
			zap.String("host", r.Host),
			zap.String("uri", r.RequestURI),
			zap.String("broadcast", m.BroadcastAddress),
			zap.String("mac", m.MAC),
			zap.Error(err),
		)
	case remaining > 0:
		m.logger.Debug("throttled magic packet",
			zap.String("host", r.Host),
			zap.String("uri", r.RequestURI),
			zap.String("broadcast", m.BroadcastAddress),
			zap.String("mac", m.MAC),
			zap.Duration("remaining", remaining),
		)
	default:
		clientIP, _ := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey).(string)
		m.logger.Info("dispatched magic packet",
			zap.String("client_ip", clientIP),
			zap.String("remote_ip", r.RemoteAddr),
			zap.String("host", r.Host),
			zap.String("uri", r.RequestURI),
			zap.String("broadcast", m.BroadcastAddress),
			zap.String("mac", m.MAC),
		)
	}
	return err
}

// send decides, records and sends under the throttle lock. It returns the
// time left in the throttle window if the packet was held back, or the error
// of a failed write.
func (m Middleware) send() (remaining time.Duration, err error) {
	throttleMu.Lock()
	defer throttleMu.Unlock()

	t := now()
	last, seen := lastSent[m.key]
	if elapsed := t.Sub(last); seen && elapsed < time.Duration(m.Throttle) {
		return time.Duration(m.Throttle) - elapsed, nil
	}

	lastSent[m.key] = t
	if _, err := m.broadcastSocket.Write(m.magicPacket); err != nil {
		// Nothing was sent, so do not hold back the next attempt.
		if seen {
			lastSent[m.key] = last
		} else {
			delete(lastSent, m.key)
		}
		return 0, err
	}
	return 0, nil
}

// UnmarshalCaddyfile implements caddyfile.Unmarshaler.
func (m *Middleware) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		args := d.RemainingArgs()

		switch len(args) {
		case 1:
			m.MAC, m.BroadcastAddress = args[0], "255.255.255.255:9"
		case 2:
			m.MAC, m.BroadcastAddress = args[0], args[1]
		default:
			return d.Err("unexpected number of arguments")
		}

		for d.NextBlock(0) {
			switch d.Val() {
			case "throttle":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := caddy.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid throttle duration %q: %v", d.Val(), err)
				}
				if dur <= 0 {
					return d.Errf("throttle must be positive, got %s", d.Val())
				}
				if d.NextArg() {
					return d.ArgErr()
				}
				m.Throttle = caddy.Duration(dur)
			default:
				return d.Errf("unrecognized subdirective %q", d.Val())
			}
		}
	}
	return nil
}

// Cleanup closes the prepared broadcast socket.
func (m *Middleware) Cleanup() error {
	// Caddy also cleans up modules whose provisioning failed, before any
	// socket was opened.
	if m.broadcastSocket == nil {
		return nil
	}
	return m.broadcastSocket.Close()
}

// parseCaddyfile unmarshals a caddyfile helper to a Middleware.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Middleware
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return m, err
}

// Interface guards
var (
	_ caddy.Provisioner           = (*Middleware)(nil)
	_ caddy.CleanerUpper          = (*Middleware)(nil)
	_ caddyhttp.MiddlewareHandler = (*Middleware)(nil)
	_ caddyfile.Unmarshaler       = (*Middleware)(nil)
)
