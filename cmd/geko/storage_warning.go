package main

import (
	"fmt"
	"io"

	"github.com/YunusEmreAlps/geko/internal/config"
	"github.com/YunusEmreAlps/geko/internal/storage"
)

func warnStorageLimit(out io.Writer, cfg config.Config, status storage.Status) {
	limit := int64(cfg.MaxDBMiB) << 20
	if limit <= 0 {
		return
	}
	if cfg.OnFull == storage.Reject && status.MainBytes > limit {
		fmt.Fprintf(out, "Warning: the physical database file is %.2f MiB, above max_db_mib (%d MiB). The reject policy refuses exports even with reusable pages. Stop Geko and run geko compact, raise max_db_mib, or choose an eviction policy.\n",
			float64(status.MainBytes)/(1<<20), cfg.MaxDBMiB)
		return
	}
	if used := status.MainBytes - status.FreeBytes; used > limit {
		fmt.Fprintf(out, "Warning: the database uses %.2f MiB, above max_db_mib (%d MiB); Geko evicts the oldest eligible traces as exports arrive.\n", float64(used)/(1<<20), cfg.MaxDBMiB)
	}
}
