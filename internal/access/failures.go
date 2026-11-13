package access

import (
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	failureWindow    = time.Minute
	maxFailureSource = 1024
)

var logFailure = log.Printf

// failureLog reports rejected credentials: the first from an address at once,
// then one count per address per minute so a flood can't fill the log. It does
// not lock addresses out; behind a proxy every client shares one address.
type failureLog struct {
	mu      sync.Mutex
	now     func() time.Time
	sources map[string]failureCount
}

type failureCount struct {
	start time.Time
	count int
}

func newFailureLog() *failureLog {
	return &failureLog{now: time.Now, sources: make(map[string]failureCount)}
}

func (l *failureLog) record(r *http.Request) {
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		address = r.RemoteAddr
	}
	forwarded := r.Header.Get("X-Forwarded-For")
	if len(forwarded) > 128 {
		forwarded = forwarded[:128]
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	current, seen := l.sources[address]
	if seen && now.Sub(current.start) < failureWindow {
		current.count++
		l.sources[address] = current
		return
	}
	if seen && current.count > 1 {
		logFailure("access: %d failed authentication attempts from %q in the minute after %s",
			current.count, address, current.start.UTC().Format(time.RFC3339))
	}
	if !seen && len(l.sources) >= maxFailureSource {
		for key, value := range l.sources {
			if now.Sub(value.start) >= failureWindow {
				delete(l.sources, key)
			}
		}
		if len(l.sources) >= maxFailureSource {
			return
		}
	}
	l.sources[address] = failureCount{start: now, count: 1}
	// X-Forwarded-For is client-controlled; it is logged quoted, for context only.
	logFailure("access: failed authentication from %q (X-Forwarded-For %q) for %s %q",
		address, forwarded, r.Method, r.URL.Path)
}
