package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YunusEmreAlps/geko/internal/investigation"
)

func TestSchema16UpgradeSharesIdentityAndPreservesOldLosses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema16.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	for _, migration := range migrations[:16] {
		if _, err := legacy.ExecContext(t.Context(), migration); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 2 {
		id := make([]byte, 16)
		id[0] = byte(i + 1)
		if _, err := legacy.ExecContext(t.Context(), "INSERT INTO traces(trace_id,first_observed_ns,last_observed_ns) VALUES(?,?,?)", id, 100+i, 200+i); err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.ExecContext(t.Context(), `INSERT INTO spans(trace_id,span_id,service_name,name,kind,start_ns,end_ns,status_code,attributes,resource_attributes)
 VALUES(?,?,'api','GET /orders',2,10,20,2,'{"http.response.status_code":503}','{"service.namespace":"shop","service.version":"1"}')`, id, make([]byte, 8)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.ExecContext(t.Context(), `INSERT INTO trace_services SELECT trace_id,service_namespace,service_name,count(*) FROM spans GROUP BY 1,2,3;
 INSERT INTO failure_group_members SELECT * FROM failure_group_member_source;
 UPDATE storage_counters SET evicted_traces=7 WHERE id=1;`+fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=16;", applicationID)); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertGroupProjection(t, store, investigation.FailureFilter{Limit: 10}, nil)
	assertGroupTotals(t, store)
	var identities, partitions, members int
	if err := store.read.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM failure_group_keys),
 (SELECT count(*) FROM failure_group_partitions),(SELECT count(*) FROM failure_group_members)`).Scan(&identities, &partitions, &members); err != nil || identities != 1 || partitions != 1 || members != 2 {
		t.Fatalf("not shared: keys=%d partitions=%d members=%d: %v", identities, partitions, members, err)
	}
	usage, err := store.StorageByApplication(t.Context())
	if err != nil || usage.EarlierEvictions != 7 || usage.EvictedTraces != 7 || len(usage.Applications) != 1 || usage.Applications[0].EvictedTraces != 0 {
		t.Fatalf("historical loss coverage: %+v: %v", usage, err)
	}
	if err := store.Prune(t.Context(), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	assertGroupTotals(t, store)
	usage, err = store.StorageByApplication(t.Context())
	if err != nil || usage.EvictedTraces != 7 || usage.EarlierEvictions != 7 {
		t.Fatalf("retention changed old losses: %+v: %v", usage, err)
	}
}

func TestSchema18UpgradeBudgetsLossesWithoutDiscardingCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger18.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	for _, migration := range migrations[:18] {
		if _, err := legacy.ExecContext(t.Context(), migration); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := legacy.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range maxEvictionApplications + 1 {
		label, count := fmt.Sprintf("app-%03d", i), 1
		if i == 0 {
			label, count = "", 7
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO application_evictions VALUES(?,?)", label, count); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO application_evictions VALUES(?,5)", strings.Repeat("界", 200)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE storage_counters SET evicted_traces=(SELECT sum(evicted_traces) FROM application_evictions)`+
		fmt.Sprintf("; PRAGMA application_id=%d; PRAGMA user_version=18;", applicationID)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	usage, err := store.StorageByApplication(t.Context())
	if err != nil || usage.EvictionOverflow != 6 || len(usage.Applications) != maxEvictionApplications {
		t.Fatalf("migration lost membership counts: %+v: %v", usage, err)
	}
	total := usage.EvictionOverflow
	for _, app := range usage.Applications {
		total += app.EvictedTraces
	}
	if total != usage.EvictedTraces {
		t.Fatalf("migration discarded historical counts: memberships=%d global=%d", total, usage.EvictedTraces)
	}
}
