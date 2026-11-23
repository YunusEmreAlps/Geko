package investigation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRunSnapshotRejectsForgedAndInconsistentGroups(t *testing.T) {
	key, _ := json.Marshal([]any{"Shop", "Shop", "api", "test", "operation", 1, "Timeout", 0})
	g := RunGroup{ID: EncodeGroupKey(string(key)), Application: "Shop", Namespace: "Shop", Service: "api", Environment: "test", Operation: "operation", Classification: "exception", ExceptionType: "Timeout", Count: 1, TraceID: strings.Repeat("a", 32), SpanID: "0100000000000000"}
	snapshot := RunSnapshot{SchemaVersion: 1, RunID: strings.Repeat("b", 32), CapturedAt: time.Now(), CollectionStatus: "available", Traces: 1, TaggedSpans: 1, FailingTraces: 1, Groups: []RunGroup{g}, Contexts: []RunContext{{Service: "api", Traces: 1}}}
	if err := ValidateRunSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Groups[0].Operation = "forged"
	if ValidateRunSnapshot(snapshot) == nil {
		t.Fatal("forged display identity accepted")
	}
	snapshot.Groups[0] = g
	snapshot.Groups = append(snapshot.Groups, g)
	if ValidateRunSnapshot(snapshot) == nil {
		t.Fatal("duplicate group accepted")
	}
	snapshot.Groups = []RunGroup{g}
	snapshot.FailingTraces = 0
	if ValidateRunSnapshot(snapshot) == nil {
		t.Fatal("inconsistent failure count accepted")
	}
}

func TestSnapshotDecodingBoundsArrays(t *testing.T) {
	for _, field := range []string{"groups", "contexts"} {
		data := []byte(`{"` + field + `":[` + strings.Repeat(`{},`, RunSnapshotLimit) + `{}]}`)
		var snapshot RunSnapshot
		if json.Unmarshal(data, &snapshot) == nil {
			t.Fatalf("oversized %s array accepted", field)
		}
	}
}
