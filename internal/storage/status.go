// Package storage defines backend diagnostics shared by local clients.
package storage

type Status struct {
	SQLiteVersion string
	SchemaVersion int
	Bytes         int64
}
