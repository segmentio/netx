package netx

import (
	"bufio"
	"context"
	"io"
	"net"
	"time"
)

// TunnelHandler is an interface that must be implemented by types that intend
// to provide logic for tunnelling connections.
//
// The ServeTunnel method is called by a Tunnel after establishing a connection
// to a remote target address.
type TunnelHandler interface {
	ServeTunnel(ctx context.Context, from net.Conn, to net.Conn)
}

// TunnelHandlerFunc makes it possible for simple function types to be used as
// connection proxies.
type TunnelHandlerFunc func(context.Context, net.Conn, net.Conn)

// ServeTunnel calls f.
func (f TunnelHandlerFunc) ServeTunnel(ctx context.Context, from net.Conn, to net.Conn) {
	f(ctx, from, to)
}

// A Tunnel is a proxy handler that establishes a second connection to a
// target address for every incoming connection it receives.
type Tunnel struct {
	// Handler is called by the tunnel when it successfully established a
	// connection to its target.
	//
	// Calling one of the tunnel's method will panic if this field is nil.
	Handler TunnelHandler

	// DialContext can be set to a dialing function to configure how the tunnel
	// establishes new connections.
	DialContext func(context.Context, string, string) (net.Conn, error)
}

// ServeProxy satisfies the ProxyHandler interface.
//
// When called the tunnel establishes a connection to target, then delegate to
// its handler.
//
// The method panics to report errors.
func (t *Tunnel) ServeProxy(ctx context.Context, from net.Conn, target net.Addr) {
	dial := t.DialContext

	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second /* safeguard */}).DialContext
	}

	to, err := dial(ctx, target.Network(), target.String())
	if err != nil {
		panic(err)
	}

	defer to.Close()
	t.Handler.ServeTunnel(ctx, from, to)
}

var (
	// TunnelRaw is the implementation of a tunnel handler which passes bytes
	// back and forth between the two ends of a tunnel.
	//
	// The implementation supports cancelltations and closes the connections
	// before it returns (because it doesn't know anything about the underlying
	// protocol being spoken and could leave the connections in an unreusable
	// state).
	TunnelRaw TunnelHandler = TunnelHandlerFunc(tunnelRaw)

	// TunnelLine is the implementation of a tunnel handler which speaks a line
	// based protocol like TELNET, expecting the client not to send more than
	// one line before getting a response.
	//
	// The implementation supports cancellations and ensures that no partial
	// lines are read from the connection.
	//
	// The maximum line length is limited to 8192 bytes.
	TunnelLine TunnelHandler = TunnelHandlerFunc(tunnelLine)
)

func tunnelRaw(ctx context.Context, from net.Conn, to net.Conn) {
	ctx, cancel := context.WithCancel(ctx)

	copy := func(w io.Writer, r io.Reader) {
		defer cancel()
		Copy(w, r)
	}

	go copy(to, from)
	go copy(from, to)

	<-ctx.Done()
	from.Close()
}

func tunnelLine(ctx context.Context, from net.Conn, to net.Conn) {
	r1 := bufio.NewReaderSize(from, 8192)
	r2 := bufio.NewReaderSize(to, 8192)

	for {
		line, err := readLine(ctx, from, r1)

		switch err {
		case nil:
		case io.EOF, context.Canceled:
			return
		default:
			fatal(from, err)
		}

		if _, err := to.Write(line); err != nil {
			fatal(from, err)
		}

		if line, err = readLine(context.Background(), to, r2); err != nil {
			fatal(from, err)
		}

		if _, err := from.Write(line); err != nil {
			fatal(from, err)
		}
	}
}
