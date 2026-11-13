// Package storage defines backend diagnostics shared by local clients.
package storage

import "errors"

var ErrCapacity = errors.New("trace storage capacity reached")

type FullPolicy string

const (
	EvictOldest FullPolicy = "evict_oldest"
	// EvictFair first evicts the oldest traces of the application, or service
	// without a namespace, that holds the most spans.
	EvictFair FullPolicy = "evict_fair"
	Reject    FullPolicy = "reject"
)

// Evicts reports whether the policy frees space by removing stored traces.
func (p FullPolicy) Evicts() bool { return p == EvictOldest || p == EvictFair }

type Status struct {
	SQLiteVersion   string
	SchemaVersion   int
	Bytes           int64
	MainBytes       int64
	WALBytes        int64
	SHMBytes        int64
	FreeBytes       int64
	EvictedTraces   int64
	RejectedExports int64
}
