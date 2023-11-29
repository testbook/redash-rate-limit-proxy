package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const RLPayload = `{
    "job": {
        "error": "You have reached the request limit. Please try again later",
        "status": 4
    }
}`

// RateLimiters holds a map of rate limiters associated with cookie tokens and a mutex for safe concurrent access
type RateLimiters struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rate     rate.Limit
	burst    int
}

// NewRateLimiters creates a new RateLimiters instance
func NewRateLimiters(rl rate.Limit, burst int) *RateLimiters {
	return &RateLimiters{
		limiters: make(map[string]*rate.Limiter),
		rate:     rl,
		burst:    burst,
	}
}

// Flusher flushes the rate limiters every 2 hours
func (rl *RateLimiters) Flusher(duration time.Duration) {
	t := time.NewTicker(duration)
	for {
		select {
		case <-t.C:
			rl.mu.Lock()
			rl.limiters = make(map[string]*rate.Limiter)
			rl.mu.Unlock()
		}
	}
}

// GetLimiter returns the rate limiter for the given token, creating a new one if necessary
func (rl *RateLimiters) GetLimiter(token string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if _, exists := rl.limiters[token]; exists {
		return rl.limiters[token]
	}

	lim := rate.NewLimiter(rl.rate, rl.burst)
	rl.limiters[token] = lim
	return lim
}

// LimitMiddleware is a middleware that rate limits based on a "remember_token" cookie
func LimitMiddleware(limiters *RateLimiters) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			remToken, err := r.Cookie("remember_token")
			if err != nil {
				w.Write([]byte("No cookie found"))
				return
			}

			var b bytes.Buffer

			r.Body = io.NopCloser(io.TeeReader(r.Body, &b))
			defer r.Body.Close()

			queryPayload := struct {
				MaxAge int64 `json:"max_age"`
			}{}

			if err = json.NewDecoder(r.Body).Decode(&queryPayload); err != nil {
				w.Write([]byte("Error parsing request body" + err.Error()))
				return
			}

			if !limiters.GetLimiter(remToken.Value).Allow() && queryPayload.MaxAge == 0 {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(RLPayload))
				return
			}

			r.Body = io.NopCloser(&b)
			next.ServeHTTP(w, r)
		})
	}
}

func main() {
	target, err := url.Parse("http://127.0.0.1:80")
	if err != nil {
		log.Fatal(err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	rateLimiters := NewRateLimiters(rate.Every(time.Minute), 15)

	go rateLimiters.Flusher(time.Minute * 120)

	http.Handle("/", LimitMiddleware(rateLimiters)(proxy))
	log.Fatal(http.ListenAndServe(":3080", nil))
}
