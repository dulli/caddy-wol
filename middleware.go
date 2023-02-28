package wakeonlan

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
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

	key             string
	logger          *zap.Logger
	pool            *caddy.UsagePool
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
	m.key = fmt.Sprintf("wol-%s", m.MAC)
	m.logger = ctx.Logger(m)
	m.pool = caddy.NewUsagePool()

	mac, err := net.ParseMAC(m.MAC)
	if err != nil {
		return err
	}
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
	_, throttled := m.pool.LoadOrStore(m.key, true)
	if throttled {
		_, err := m.pool.Delete(m.key)
		if err != nil {
			return err
		}
	} else {
		m.logger.Info("dispatched magic packet",
			zap.String("mac", m.MAC),
		)
		_, err := m.broadcastSocket.Write(m.magicPacket)
		if err != nil {
			return err
		}
		time.AfterFunc(10*time.Minute, func() {
			_, _ = m.pool.Delete(m.key)
		})
	}
	return next.ServeHTTP(w, r)
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
	}
	return nil
}

// Cleanup closes the prepared broadcast socket.
func (m *Middleware) Cleanup() error {
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
