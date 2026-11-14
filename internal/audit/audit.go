// Package audit appends one JSON line per change and sign-in, naming who acted.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
)

// Event is one audit log line. It never holds a token, a session ID or telemetry text.
type Event struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"`
	Actor  string    `json:"actor"`
	Role   string    `json:"role,omitempty"`
	Via    string    `json:"via,omitempty"`
	Remote string    `json:"remote,omitempty"`
	Target string    `json:"target,omitempty"`
	// Detail holds small structured values such as the new group state.
	Detail map[string]string `json:"detail,omitempty"`
	Result string            `json:"result"`
}

// Log writes events. A nil Log discards them.
type Log struct {
	mu  sync.Mutex
	out io.Writer
	now func() time.Time
}

// Open appends to path, or writes to standard error when path is "-". Call the
// returned function when the server stops.
func Open(path string) (*Log, func() error, error) {
	if path == "-" {
		return New(os.Stderr), func() error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open audit log: %w", err)
	}
	return New(file), file.Close, nil
}

func New(out io.Writer) *Log {
	return &Log{out: out, now: time.Now}
}

// Record writes event for the caller in ctx. A failed write is logged, not
// returned: the change it describes has already happened.
func (l *Log) Record(ctx context.Context, r *http.Request, event Event) {
	if l == nil {
		return
	}
	if event.Actor == "" {
		principal, ok := access.Caller(ctx)
		if !ok {
			principal = access.Local
		}
		event.Actor, event.Role, event.Via = principal.Name, principal.Role.String(), principal.Via
	}
	if event.Result == "" {
		event.Result = "ok"
	}
	if r != nil {
		event.Remote = remote(r)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	event.Time = l.now().UTC()
	line, err := json.Marshal(event)
	if err == nil {
		_, err = l.out.Write(append(line, '\n'))
	}
	if err != nil {
		log.Printf("audit: %v", err)
	}
}

// remote is the peer address, plus the first forwarded address a proxy reported.
func remote(r *http.Request) string {
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		address = r.RemoteAddr
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if len(forwarded) > 128 {
			forwarded = forwarded[:128]
		}
		address += " (forwarded for " + forwarded + ")"
	}
	return address
}
