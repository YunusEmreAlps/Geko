// Package storage defines backend diagnostics shared by local clients.
package storage

import "errors"

var ErrCapacity = errors.New("trace storage capacity reached")

type FullPolicy string

const (
	EvictOldest FullPolicy = "evict_oldest"
	Reject      FullPolicy = "reject"
)

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
