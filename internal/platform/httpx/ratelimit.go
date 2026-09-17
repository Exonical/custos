package httpx

import (
	"net/http"
	"sync"
	"time"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// PrincipalRateLimiter is a per-principal token-bucket middleware for
// expensive endpoints (script validate/import) — separate from the
// general bucket per docs/script-validation.md §API.
type PrincipalRateLimiter struct {
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
	maxKeys int

	mu      sync.Mutex
	buckets map[string]*bucket
	order   []string // FIFO eviction
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewPrincipalRateLimiter returns a limiter allowing `per` requests per
// minute with the given burst. maxKeys bounds the map (default 4096).
func NewPrincipalRateLimiter(per float64, burst int, maxKeys int) *PrincipalRateLimiter {
	if per <= 0 {
		per = 30
	}
	if burst <= 0 {
		burst = 10
	}
	if maxKeys <= 0 {
		maxKeys = 4096
	}
	return &PrincipalRateLimiter{
		rate: per / 60, burst: float64(burst), maxKeys: maxKeys,
		now:     func() time.Time { return time.Now() },
		buckets: make(map[string]*bucket),
	}
}

func (l *PrincipalRateLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			delete(l.buckets, l.order[0])
			l.order = l.order[1:]
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
		l.order = append(l.order, key)
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	b.last = now
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// KeyFunc extracts the bucket key from the request (e.g. principal user
// ID). A key func returning "" bypasses limiting.
type KeyFunc func(r *http.Request) string

// Middleware 429s requests whose principal bucket is empty.
func (l *PrincipalRateLimiter) Middleware(key KeyFunc) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k != "" && !l.allow(k) {
				WriteError(r.Context(), w, apperr.New(
					apperr.RateLimited, "RATE_LIMITED",
					"validation rate limit exceeded"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
