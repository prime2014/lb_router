package router

import (
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	mu            sync.Mutex
	RequestWindow time.Duration
	MaxReqCount   int
	ipRequest     map[string][]time.Time
}

func NewRateLimiter(requestWindow time.Duration, reqCount int) *RateLimiter {
	return &RateLimiter{
		RequestWindow: requestWindow,
		MaxReqCount:   reqCount,
		ipRequest:     make(map[string][]time.Time),
	}
}

func (r *RateLimiter) SlidingWindow(ipAddr string) bool {
	currentTime := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	reqs := r.ipRequest[ipAddr]

	windowStart := currentTime.Add(-r.RequestWindow)
	validReqs := make([]time.Time, 0, len(reqs))

	for _, t := range reqs {
		if t.After(windowStart) {
			validReqs = append(validReqs, t)
		}
	}

	if len(validReqs) >= r.MaxReqCount {
		r.ipRequest[ipAddr] = validReqs
		return false
	}

	validReqs = append(validReqs, currentTime)
	r.ipRequest[ipAddr] = validReqs
	return true
}

func (r *RateLimiter) RateLimiterMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ip := req.RemoteAddr

		if !r.SlidingWindow(ip) {
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, req)
	})
}
