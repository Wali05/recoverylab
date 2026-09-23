package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"recoverylab/internal/config"
)

type proxyResult struct {
	status int
	err    error
}

type dropProxy struct {
	listener net.Listener
	server   *http.Server
	result   chan proxyResult
	once     sync.Once
	used     atomic.Bool
}

func newDropProxy(ctx context.Context, target config.Request) (*dropProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &dropProxy{listener: listener, result: make(chan proxyResult, 1)}
	p.server = &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		if !p.used.CompareAndSwap(false, true) {
			http.Error(w, "fault proxy accepts one operation", http.StatusTooManyRequests)
			return
		}
		if incoming.Method != strings.ToUpper(target.Method) {
			p.once.Do(func() { p.result <- proxyResult{err: fmt.Errorf("client method changed before proxy")} })
			http.Error(w, "method mismatch", http.StatusBadRequest)
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(incoming.Body, (1<<20)+1))
		if readErr != nil || !bytes.Equal(body, target.Body) {
			p.once.Do(func() { p.result <- proxyResult{err: fmt.Errorf("client body changed or exceeded 1 MiB before proxy")} })
			http.Error(w, "body mismatch", http.StatusBadRequest)
			return
		}
		status, forwardErr := send(ctx, target, target.URL)
		if forwardErr != nil {
			p.once.Do(func() { p.result <- proxyResult{status: status, err: forwardErr} })
			http.Error(w, "upstream failed", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			p.once.Do(func() { p.result <- proxyResult{err: fmt.Errorf("HTTP connection cannot be hijacked")} })
			http.Error(w, "connection cannot be dropped", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			p.once.Do(func() { p.result <- proxyResult{err: err} })
			return
		}
		p.once.Do(func() { p.result <- proxyResult{status: status} })
		_ = conn.Close()
	})}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *dropProxy) URL() string { return "http://" + p.listener.Addr().String() + "/" }

func (p *dropProxy) Close() {
	_ = p.server.Close()
	_ = p.listener.Close()
}
