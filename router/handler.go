package router

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
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
	counter   uint64
	algorithm RoutingType
	ctx       context.Context
	urls      []string
	mode      ConnectionType
	ring      *ConsistentHashRing
}

func NewRouterHandler(cfg RouterConfig, ctx context.Context) *RouterHandler {
	algorithm := ParseRoutingType(cfg.Algorithm)
	mode := ParseConnectionType(cfg.Mode)

	handler := &RouterHandler{
		urls:      cfg.URLs,
		algorithm: algorithm,
		ctx:       ctx,
		mode:      mode,
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
				log.Println("Server shutting down, waiting for active connections...")
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

func (r *RouterHandler) StartHTTPServer(listenAddr string, certFile, keyFile string) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", r.serveHTTPProxy)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		IdleTimeout:       5 * time.Minute,
		ReadHeaderTimeout: time.Minute,
	}

	// Graceful shutdown listener
	go func() {
		<-r.ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if certFile != "" && keyFile != "" {
		log.Printf("[HTTPS Mode] TLS enabled using cert: %s", certFile)
		if err := server.ListenAndServeTLS(certFile, keyFile); err != http.ErrServerClosed {
			log.Printf("[Xpress] Server error: %v", err)
			return err
		}
	} else {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("[Xpress] Server error: %v", err)
			return err
		}
	}

	return nil
}

func (r *RouterHandler) serveHTTPProxy(w http.ResponseWriter, req *http.Request) {
	// extract client IP (handling upstream X-Forwarded-For if behind another edge proxy)
	clientIP, _, _ := net.SplitHostPort(req.RemoteAddr)
	reqCtx := context.WithValue(req.Context(), "client_ip", clientIP)

	targetServer := r.SelectServer(reqCtx)
	targetURL, err := url.Parse(targetServer)

	if err != nil {
		http.Error(w, "Bad Gateway Target", http.StatusBadGateway)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	proxy.Rewrite = func(pr *httputil.ProxyRequest) {
		// Set the target scheme/host (equivalent to the default Driector behaviour)
		pr.SetXForwarded()

		pr.SetURL(targetURL)

		pr.Out.Host = pr.In.Host
	}

	proxy.ServeHTTP(w, req)
}

func (r *RouterHandler) StartServer(listenAddr string, certFile, keyFile string) error {

	switch r.mode {
	case HttpMode:
		return r.StartHTTPServer(listenAddr, certFile, keyFile)
	case TcpMode:
		return r.StartTCPServer(listenAddr)
	default:
		return fmt.Errorf("unsupported connection mode %v", r.mode)
	}
}
