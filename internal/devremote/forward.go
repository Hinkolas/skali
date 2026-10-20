package devremote

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Forwarder relays every connection a listener accepts to one TCP target,
// byte for byte. It never looks inside: TLS ends at the remote edge, so
// SNI, Host, Origin, and cookies arrive as the browser sent them, and
// WebSockets, server-sent events, and HTTP/2 need nothing special.
//
// Each connection dials the target anew. A forwarder therefore survives a
// laptop's sleep, a network change, or a remote that comes up after it:
// the next connection simply reaches it again.
type Forwarder struct {
	// Target is the host:port every connection is relayed to.
	Target string
	// Dial connects to Target; nil uses a plain TCP dialer.
	Dial dialFunc
	// Logf reports reachability changes: the first failed dial, a reminder
	// while the target stays unreachable, and its return. Nil is silent.
	Logf func(format string, args ...any)
	// Debugf reports copy failures and streams that end without carrying
	// any bytes. It is enabled by the CLI's --verbose flag. Nil is silent.
	Debugf func(format string, args ...any)

	mu       sync.Mutex
	down     bool
	reported time.Time
}

// unreachableReminder spaces repeated reports about a target that stays
// unreachable while connections keep arriving.
const unreachableReminder = 30 * time.Second

// Serve accepts connections on listener until ctx ends, then closes the
// listener and every relayed connection and returns once they are gone.
func (f *Forwarder) Serve(ctx context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var relays sync.WaitGroup
	defer relays.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Out of file descriptors and the like: back off instead of
			// spinning, and keep serving once it passes.
			f.logf("accept on %s: %v", listener.Addr(), err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		relays.Go(func() { f.relay(ctx, conn) })
	}
}

func (f *Forwarder) relay(ctx context.Context, client net.Conn) {
	defer client.Close()
	dial := f.Dial
	if dial == nil {
		dial = defaultDialer.DialContext
	}
	upstream, err := dial(ctx, "tcp", f.Target)
	if err != nil {
		if ctx.Err() == nil {
			f.failed(err)
		}
		return
	}
	f.succeeded()
	defer upstream.Close()
	stop := context.AfterFunc(ctx, func() {
		client.Close()
		upstream.Close()
	})
	defer stop()
	var both sync.WaitGroup
	copyStream := func(dst, src net.Conn, direction string) {
		n, err := pipe(dst, src)
		if f.Debugf != nil && ctx.Err() == nil && (err != nil || n == 0) {
			if err == nil {
				err = io.EOF
			}
			f.Debugf("relay %s to %s (%s) ended after %d bytes: %v", client.RemoteAddr(), f.Target, direction, n, err)
		}
	}
	both.Go(func() { copyStream(upstream, client, "client -> target") })
	both.Go(func() { copyStream(client, upstream, "target -> client") })
	both.Wait()
}

// pipe copies src to dst until src's stream ends, then half-closes dst so
// the far side sees the end while the other direction keeps flowing, the
// way a direct connection behaves. A broken direction tears down both.
func pipe(dst, src net.Conn) (int64, error) {
	n, err := io.Copy(dst, src)
	if err != nil {
		dst.Close()
		src.Close()
		return n, err
	}
	if closer, ok := dst.(interface{ CloseWrite() error }); ok {
		if err := closer.CloseWrite(); err != nil {
			dst.Close()
			return n, err
		}
		return n, nil
	}
	return n, dst.Close()
}

func (f *Forwarder) failed(err error) {
	f.mu.Lock()
	now := time.Now()
	report := !f.down || now.Sub(f.reported) >= unreachableReminder
	f.down = true
	if report {
		f.reported = now
	}
	f.mu.Unlock()
	if report {
		f.logf("cannot reach %s: %v", f.Target, err)
	}
}

func (f *Forwarder) succeeded() {
	f.mu.Lock()
	recovered := f.down
	f.down = false
	f.mu.Unlock()
	if recovered {
		f.logf("reaching %s again", f.Target)
	}
}

func (f *Forwarder) logf(format string, args ...any) {
	if f.Logf != nil {
		f.Logf(format, args...)
	}
}
