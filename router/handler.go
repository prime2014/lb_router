package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/acme/autocert"
)

type RoutingType int

const (
	RoundRobin RoutingType = iota + 1
	LeastConnection
	LeastResponseTime
	IPHash
	ConsistentHash
)

type ConnectionType int

const (
	HttpMode ConnectionType = iota + 1
	TcpMode
)

type ConsistentHashRing struct {
	mu       sync.RWMutex
	vnodes   int               // number of virtual nodes and key distribution
	ring     []uint32          // Sorted list of virtual node hash values
	nodesMap map[uint32]string // Maps virtual node hash -> physical URL
}

func NewConsistentHashRing(vnodes int) *ConsistentHashRing {
	if vnodes <= 0 {
		vnodes = 100
	}

	return &ConsistentHashRing{
		vnodes:   vnodes,
		nodesMap: make(map[uint32]string),
	}
}

func (c *ConsistentHashRing) hash(key string) uint32 {
	hasher := fnv.New32a()
	hasher.Write([]byte(key))
	return hasher.Sum32()
}

// AddNode adds a physical backend server and its virtual nodes to the ring
func (c *ConsistentHashRing) AddNode(nodeURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := 0; i < c.vnodes; i++ {
		vnodeKey := nodeURL + "#" + strconv.Itoa(i)
		vhash := c.hash(vnodeKey)
		c.ring = append(c.ring, vhash)
		c.nodesMap[vhash] = nodeURL
	}

	sort.Slice(c.ring, func(i, j int) bool {
		return c.ring[i] < c.ring[j]
	})
}

func (c *ConsistentHashRing) GetNode(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.ring) == 0 {
		return "", false
	}

	khash := c.hash(key)

	idx := sort.Search(len(c.ring), func(i int) bool {
		return c.ring[i] >= khash
	})

	// Wrap around to 0 if the key hash is greater than all nodes on the ring
	if idx == len(c.ring) {
		idx = 0
	}

	nodeURL, exists := c.nodesMap[c.ring[idx]]

	return nodeURL, exists
}

type RouterHandler struct {
	mu          sync.RWMutex
	counter     uint64
	algorithm   RoutingType
	ctx         context.Context
	urls        []string
	mode        ConnectionType
	ring        *ConsistentHashRing
	connection  map[string]*atomic.Uint64
	rateLimiter *RateLimiter
	firewall    *FirewallConfig
}

func NewRouterHandler(cfg RouterConfig, ctx context.Context) *RouterHandler {
	algorithm := ParseRoutingType(cfg.Algorithm)
	mode := ParseConnectionType(cfg.Mode)

	// 1. Pre-initialize the connection counters for each backend URL
	connMap := make(map[string]*atomic.Uint64)
	for _, u := range cfg.URLs {
		connMap[u] = &atomic.Uint64{}
	}

	// 2. Build and populate the Firewall configuration safely
	blockedMap := make(map[string]bool)
	for _, ips := range cfg.BlockedIps {
		blockedMap[ips] = true
	}

	firewallCfg := &FirewallConfig{
		BlockedIps:  blockedMap,
		MaxBodySize: cfg.MaxBodySize,
	}

	// 3. Construct the RouterHandler with all pointers safely allocated
	handler := &RouterHandler{
		urls:        cfg.URLs,
		algorithm:   algorithm,
		ctx:         ctx,
		mode:        mode,
		connection:  connMap,
		rateLimiter: NewRateLimiter(time.Minute, 1000),
		firewall:    firewallCfg, // Assigned here so it's never nil!
	}

	// Initialize ring if consistent hashing is selected
	if algorithm == ConsistentHash {
		handler.ring = NewConsistentHashRing(150)
		for _, nodeURL := range cfg.URLs {
			handler.ring.AddNode(nodeURL)
		}
	}

	return handler
}

func (r *RouterHandler) SelectServer(ctx context.Context) string {
	// implementation pending
	switch r.algorithm {
	case RoundRobin:
		return r.selectRoundRobin()
	case ConsistentHash:
		return r.selectConsistentHash(ctx)
	case LeastConnection:
		return r.selectLeastConnection(ctx)
	case IPHash:
		return r.selectIPHash(ctx)
	default:
		return r.selectRoundRobin()
	}
}

func (r *RouterHandler) selectRoundRobin() string {
	// atomically increment counter int
	n := uint64(len(r.urls))
	myCounter := atomic.AddUint64(&r.counter, 1) - 1
	return r.urls[myCounter%n]
}

func (r *RouterHandler) selectLeastConnection(ctx context.Context) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.connection) == 0 {
		return ""
	}

	var minServer string
	var minCount uint64 = math.MaxUint64

	for server, count := range r.connection {
		if count.Load() < minCount {
			minCount = count.Load()
			minServer = server
		}
	}

	return minServer
}
func (r *RouterHandler) selectConsistentHash(ctx context.Context) string {
	// Attempt to pull client IP
	key, ok := ctx.Value("client_ip").(string)

	if !ok || key == "" {
		// Try to fallback to round robin
		return r.selectRoundRobin()
	}

	if nodeURL, ok := r.ring.GetNode(key); ok {
		return nodeURL
	}

	return r.selectRoundRobin()
}

func (r *RouterHandler) selectIPHash(ctx context.Context) string {
	// Example retrieval of client IP from context
	clientIP, ok := ctx.Value("client_ip").(string)

	if !ok || clientIP == "" {
		return r.selectRoundRobin()
	}

	hasher := fnv.New32a()
	hasher.Write([]byte(clientIP))
	idx := uint64(hasher.Sum32()) % uint64(len(r.urls))
	return r.urls[idx]
}

func (r *RouterHandler) StartTCPServer(url string) error {
	conn := NewConnectionPool(url, 10)

	var wg sync.WaitGroup

	addr, err := net.ResolveTCPAddr("tcp", url)

	if err != nil {
		log.Printf("Could not resolve tcp network address: %v", err)
		return nil
	}

	listener, err := net.ListenTCP("tcp", addr)

	if err != nil {
		return fmt.Errorf("listener error: %w", err)
	}

	defer listener.Close()

	log.Printf("Load balancer listening on %s", url)

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			select {
			case <-r.ctx.Done():
				log.Info().Msg("[Xpress]: Server shutting down, waiting for active connections...")
				wg.Wait()
				return nil
			default:
				log.Printf("Accept error: %v", err)
				continue
			}
		}

		// handle each incoming connection concurrently in its own goroutine
		wg.Add(1)
		go func(myConn net.Conn) {
			defer wg.Done()

			// create a child context with a 10-second timeout derived from r.ctx
			reqCtx, cancel := context.WithTimeout(r.ctx, 10*time.Second)

			defer cancel()

			conn.ProxyRequest(reqCtx, myConn)
		}(clientConn)
	}
}

func (r *RouterHandler) StartHTTPServer(
	listenAddr string,
	certFile, keyFile string,
	domains []string,
	email string,
) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", r.serveHTTPProxy)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		IdleTimeout:       5 * time.Minute,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// Graceful shutdown listener
	go func() {
		<-r.ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if len(domains) > 0 {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Email:      email,
			HostPolicy: autocert.HostWhitelist(domains...),
			Cache:      autocert.DirCache("/var/lib/xpress/certs"),
		}

		go func() {
			httpSrv := &http.Server{
				Addr:    ":80",
				Handler: m.HTTPHandler(nil),
			}

			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("[Xpress] ACME HTTP challenge server failed")
			}
		}()

		server.TLSConfig = &tls.Config{
			GetCertificate: m.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
		log.Info().
			Strs("domains", domains).
			Str("email", email).
			Msg("[Xpress] Starting automatic HTTPS with Let's Encrypt")

		// Empty strings → uses GetCertificate from the manager
		if err := server.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[Xpress] Server error")
			return err
		}
		return nil
	}

	if certFile != "" && keyFile != "" {
		log.Printf("[HTTPS Mode] TLS enabled using cert: %s", certFile)
		if err := server.ListenAndServeTLS(certFile, keyFile); err != http.ErrServerClosed {
			log.Printf("[Xpress] Server error: %v", err)
			return err
		}
		return nil
	}

	log.Info().Msg("[Xpress] Starting plain HTTP (no TLS)")
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Error().Err(err).Msg("[Xpress] Server error")
		return err
	}
	return nil
}

func (r *RouterHandler) serveHTTPProxy(w http.ResponseWriter, req *http.Request) {
	// extract client IP (handling upstream X-Forwarded-For if behind another edge proxy)
	clientIP, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		clientIP = req.RemoteAddr
	}

	if req.Body != nil {
		req.Body = http.MaxBytesReader(w, req.Body, r.firewall.MaxBodySize)
	}

	if r.firewall.ValidateblockedIps(clientIP) {
		http.Error(w, "Forbidden: Ip blocked", http.StatusForbidden)
		return
	}

	// check rate limiter using the persistent instance
	if !r.rateLimiter.SlidingWindow(clientIP) {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
	}

	// buffer request body if present so it can be re-read on retries
	if req.Body != nil && req.GetBody == nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err == nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			req.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(bodyBytes)), nil
			}
		}
	}

	reqCtx := context.WithValue(req.Context(), "client_ip", clientIP)
	req = req.WithContext(reqCtx)

	proxy := &httputil.ReverseProxy{
		Director: func(pr *http.Request) {
			// Director is called before Transport; we set X-Forwarded headers here
			if _, ok := pr.Header["X-Forwarded-For"]; !ok {
				pr.Header.Set("X-Forwarded-For", clientIP)
			}
			if pr.Header.Get("User-Agent") == "" {
				pr.Header.Set("User-Agent", "Xpress-LoadBalancer/1.0")
			}
		},
		Transport: &RetryTransport{router: r},
		ModifyResponse: func(resp *http.Response) error {
			statusCode := resp.StatusCode
			event := log.Info()
			if statusCode >= 500 {
				event = log.Error()
			} else if statusCode >= 400 {
				event = log.Warn()
			}

			event.
				Int("Status", statusCode).
				Str("Method", req.Method).
				Str("Protocol", req.Proto).
				Str("URL", req.URL.Path).
				Msg("[Xpress]")

			return nil
		},
		ErrorHandler: func(rw http.ResponseWriter, r *http.Request, err error) {
			log.Error().Err(err).Msg("[Xpress] Proxy exhausted all backend retries")
			http.Error(w, "Bad Gateway - All backends failed", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, req)
}

type proxyResponseRecorder struct {
	http.ResponseWriter
	statusCode    int
	headerWritten bool
}

func (rec *proxyResponseRecorder) WriteHeader(code int) {
	if rec.headerWritten {
		return
	}
	rec.statusCode = code
	rec.headerWritten = true

	// CRITICAL FIX: Pass the status code down to the real client response writer!
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *proxyResponseRecorder) Write(b []byte) (int, error) {
	if !rec.headerWritten {
		rec.WriteHeader(http.StatusOK)
	}
	return rec.ResponseWriter.Write(b)
}

func (r *RouterHandler) StartServer(listenAddr string, certFile, keyFile string) error {

	switch r.mode {
	case HttpMode:
		return r.StartHTTPServer(listenAddr, certFile, keyFile, nil, "xpress@mail.com")
	case TcpMode:
		return r.StartTCPServer(listenAddr)
	default:
		return fmt.Errorf("unsupported connection mode %v", r.mode)
	}
}
