package server

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limit allows Events per Per on average, with bursts of up to Burst. A zero Limit means unlimited.
type Limit struct {
	Events int
	Per    time.Duration
	Burst  int
}

// Limits configures each rate limiter. Counters live in memory, so they reset on restart
// and are per process (fine for a single server instance).
type Limits struct {
	API      Limit // every /api and /auth request, per client IP
	Auth     Limit // Google sign-in starts, per client IP
	Write    Limit // likes, favourites, deletes, renames: per user
	Meme     Limit // saved memes, per user
	Template Limit // uploaded templates, per user
}

func DefaultLimits() Limits {
	return Limits{
		API:      Limit{Events: 300, Per: time.Minute, Burst: 100},
		Auth:     Limit{Events: 20, Per: time.Minute, Burst: 20},
		Write:    Limit{Events: 120, Per: time.Minute, Burst: 60},
		Meme:     Limit{Events: 60, Per: time.Hour, Burst: 10},
		Template: Limit{Events: 20, Per: time.Hour, Burst: 5},
	}
}

type limiters struct {
	api, auth, write, meme, template *limiter
}

// limiter is a token bucket per key.
type limiter struct {
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(l Limit, now func() time.Time) *limiter {
	if l.Events <= 0 || l.Per <= 0 {
		return nil
	}
	burst := max(l.Burst, 1)
	return &limiter{
		rate:    float64(l.Events) / l.Per.Seconds(),
		burst:   float64(burst),
		now:     now,
		buckets: map[string]*bucket{},
	}
}

// allow takes a token for key, or reports how long until one is available.
func (l *limiter) allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	t := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Drop buckets that have refilled completely, so memory stays bounded.
	if t.Sub(l.swept) > time.Minute {
		for k, b := range l.buckets {
			if b.tokens+t.Sub(b.last).Seconds()*l.rate >= l.burst {
				delete(l.buckets, k)
			}
		}
		l.swept = t
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: t}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+t.Sub(b.last).Seconds()*l.rate)
	b.last = t
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

func tooManyRequests(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many requests, try again in "+humanWait(secs))
}

func humanWait(secs int) string {
	switch {
	case secs < 60:
		return strconv.Itoa(max(secs, 1)) + "s"
	case secs < 3600:
		return strconv.Itoa((secs+59)/60) + " min"
	default:
		return strconv.Itoa((secs+3599)/3600) + " h"
	}
}

// clientIP identifies the caller. With TrustProxy, the right-most X-Forwarded-For entry is used:
// that is the address the (trusted) reverse proxy saw, which the client cannot spoof.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitIP rate-limits a handler by client IP.
func (s *Server) limitIP(l *limiter, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := l.allow(s.clientIP(r)); !ok {
			tooManyRequests(w, wait)
			return
		}
		h(w, r)
	}
}

// limitUser rate-limits an authenticated handler by user.
func limitUser(l *limiter, h func(http.ResponseWriter, *http.Request, *User)) func(http.ResponseWriter, *http.Request, *User) {
	return func(w http.ResponseWriter, r *http.Request, u *User) {
		if ok, wait := l.allow(strconv.FormatInt(u.ID, 10)); !ok {
			tooManyRequests(w, wait)
			return
		}
		h(w, r, u)
	}
}
