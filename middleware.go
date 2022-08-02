package wakeonlan

import (
	"fmt"
	"net"
	"net/http"

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

type Middleware struct {
	MAC string `json:"mac,omitempty"`

	logger          *zap.Logger
	magicPacket     []byte
	broadcastSocket net.Conn
}

func (Middleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.wake_on_lan",
		New: func() caddy.Module { return new(Middleware) },
	}
}

func (m *Middleware) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger(m)
	mac, err := net.ParseMAC(m.MAC)
	if err != nil {
		return err
	}
	m.magicPacket = BuildMagicPacket(mac)
	if err != nil {
		return err
	}
	m.broadcastSocket, err = net.Dial("udp", "255.255.255.255:9")
	if err != nil {
		return err
	}
	return nil
}

func (m Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// TODO: throttle magic packet sending to not flood the network
	m.logger.Info("dispatched magic packet",
		zap.String("packet", fmt.Sprintf("0x%x", m.magicPacket)),
	)
	_, err := m.broadcastSocket.Write(m.magicPacket)
	if err != nil {
		return err
	}
	return next.ServeHTTP(w, r)
}

func (m *Middleware) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if !d.Args(&m.MAC) {
			return d.ArgErr()
		}
	}
	return nil
}

func (m *Middleware) Cleanup() error {
	return m.broadcastSocket.Close()
}

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
