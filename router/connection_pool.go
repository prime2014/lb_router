package router

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/pires/go-proxyproto"
)

type ConnectionPool struct {
	mu         sync.Mutex
	target     string
	conns      chan net.Conn
	capacity   int
	bufferPool sync.Pool
}

func NewConnectionPool(target string, capacity int) *ConnectionPool {
	return &ConnectionPool{
		target:   target,
		capacity: capacity,
		conns:    make(chan net.Conn, capacity),
		bufferPool: sync.Pool{
			New: func() any {
				// allocate a 32KB buffer once per cpu core/thread
				b := make([]byte, 32*1024)
				return &b
			},
		},
	}
}

// Get retrieves an idle connection or dials a new one
func (p *ConnectionPool) Get() (net.Conn, error) {
	select {
	case conns := <-p.conns:
		return conns, nil
	default:
		return net.DialTimeout("tcp", p.target, 3*time.Second)
	}
}

func (p *ConnectionPool) Put(conn net.Conn) {
	if conn == nil {
		return
	}

	select {
	case p.conns <- conn:
		// returned to pool successfully
	default:
		// Pool is full; close the connection
		conn.Close()
	}
}

func (p *ConnectionPool) ProxyRequest(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		clientConn.SetDeadline(deadline)
	}

	// Get healthy backend connection pool
	backendConn, err := p.Get()
	if err != nil {
		log.Printf("Failed to get backend connection: %v", err)
		return
	}

	if deadline, ok := ctx.Deadline(); ok {
		backendConn.SetDeadline(deadline)
	}

	isValid, ok := ctx.Value("proxyProtocol").(bool)

	if !ok {
		isValid = false
	}

	// Build and send PROXY header (v2 is recommended)

	if isValid {
		header := proxyproto.HeaderProxyFromAddrs(
			2,
			clientConn.RemoteAddr(),
			backendConn.LocalAddr(),
		)

		if _, err := header.WriteTo(backendConn); err != nil {
			return
		}
	}

	// track whether connection is healthy before returning to pool
	var connErr error
	defer func() {
		if connErr != nil {
			backendConn.Close()
			p.Put(nil)
		} else {
			p.Put(backendConn)
		}
	}()

	// Fetch buffers from sync.Pool
	buf1Ptr := p.bufferPool.Get().(*[]byte)
	buf2Ptr := p.bufferPool.Get().(*[]byte)
	defer p.bufferPool.Put(buf1Ptr)
	defer p.bufferPool.Put(buf2Ptr)

	var wg sync.WaitGroup
	wg.Add(2)

	// Direction Client -> Backend
	go func() {
		defer wg.Done()

		_, err := io.CopyBuffer(backendConn, clientConn, *buf1Ptr)
		if err != nil {
			connErr = err
		}

		if tc, ok := backendConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	// Direction Backend -> Client
	go func() {
		defer wg.Done()

		_, err := io.CopyBuffer(clientConn, backendConn, *buf2Ptr)
		if err != nil {
			connErr = err
		}

		if tc, ok := clientConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	wg.Wait()
}
