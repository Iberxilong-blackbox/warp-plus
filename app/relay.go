package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
)

type tcpRelay struct {
	ctx      context.Context
	listener net.Listener
	logger   *slog.Logger

	mu       sync.RWMutex
	upstream string
}

func startTCPRelay(ctx context.Context, l *slog.Logger, bind netip.AddrPort, upstream netip.AddrPort) (*tcpRelay, netip.AddrPort, error) {
	listener, err := net.Listen("tcp", bind.String())
	if err != nil {
		return nil, netip.AddrPort{}, err
	}

	relay := &tcpRelay{
		ctx:      ctx,
		listener: listener,
		logger:   l.With("subsystem", "egress-relay"),
		upstream: upstream.String(),
	}

	go relay.serve()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	return relay, listener.Addr().(*net.TCPAddr).AddrPort(), nil
}

func (r *tcpRelay) setUpstream(upstream netip.AddrPort) {
	r.mu.Lock()
	r.upstream = upstream.String()
	r.mu.Unlock()
}

func (r *tcpRelay) currentUpstream() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.upstream
}

func (r *tcpRelay) serve() {
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-r.ctx.Done():
				return
			default:
				r.logger.Error("relay accept failed", "error", err)
				continue
			}
		}

		go r.handle(conn)
	}
}

func (r *tcpRelay) handle(client net.Conn) {
	defer client.Close()

	var dialer net.Dialer
	upstream, err := dialer.DialContext(r.ctx, "tcp", r.currentUpstream())
	if err != nil {
		r.logger.Warn("relay upstream dial failed", "error", err)
		return
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		done <- struct{}{}
	}()
	<-done
}
