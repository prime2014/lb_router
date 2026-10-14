package router

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/rs/zerolog/log"
)

type RetryTransport struct {
	router *RouterHandler
}

func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	maxRetries := 3
	if maxRetries > len(t.router.urls) {
		maxRetries = len(t.router.urls)
	}

	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Clone the request so we can safely mutate URL each attempt
		reqCopy := req.Clone(req.Context())

		targetServer := t.router.SelectServer(req.Context())
		targetURL, err := url.Parse(targetServer)
		if err != nil {
			lastErr = err
			continue
		}

		reqCopy.URL.Scheme = targetURL.Scheme
		reqCopy.URL.Host = targetURL.Host
		reqCopy.Host = targetURL.Host // important!

		// Rewind body if needed
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				lastErr = err
				continue
			}
			reqCopy.Body = body
		}

		resp, err := http.DefaultTransport.RoundTrip(reqCopy)
		if err != nil {
			lastErr = err
			log.Error().
				Err(err).
				Str("Target", targetServer).
				Int("Attempt", attempt+1).
				Msg("[Xpress] Connection error, retrying...")
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("backend returned %d", resp.StatusCode)
			log.Error().
				Int("Status", resp.StatusCode).
				Str("Target", targetServer).
				Int("Attempt", attempt+1).
				Msg("[Xpress] 5xx, retrying...")
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("all backend retries failed: %w", lastErr)
}
