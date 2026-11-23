package otlp

import (
	"encoding/hex"
	"slices"
	"time"

	"github.com/YunusEmreAlps/geko/internal/telemetry"
)

const maxConnectionServices = 128
const connectionHistory = 5 * time.Minute

// ServiceObservation is a representative span from the latest committed export
// for a service identity. It is not a complete list of versions or run IDs.
// It contains normalized resource metadata only, never credentials or events.
type ServiceObservation struct {
	Namespace, Service, Environment, Version, RunID, TraceID string
	At                                                       time.Time
}

type ConnectionSnapshot struct {
	Services []ServiceObservation
	// Replacements discloses identities displaced from the bounded recent cache.
	Replacements uint64
}

type connectionIdentity struct{ namespace, service, environment string }

// recordConnections runs with h.mu held, after a successful storage commit.
func (h *Receiver) recordConnections(at time.Time, spans []telemetry.Span) {
	if h.connections == nil {
		h.connections = make(map[connectionIdentity]ServiceObservation)
	}
	for _, span := range spans {
		attr := func(key string) string { value, _ := span.ResourceAttributes[key].(string); return value }
		environment := attr("deployment.environment.name")
		if environment == "" {
			environment = attr("deployment.environment")
		}
		key := connectionIdentity{attr("service.namespace"), span.ServiceName, environment}
		// One representative per identity/export avoids allocating IDs for every
		// span in the common case of a single-service batch.
		if previous, ok := h.connections[key]; ok && previous.At.Equal(at) {
			continue
		}
		if _, ok := h.connections[key]; !ok && len(h.connections) >= maxConnectionServices {
			var oldest connectionIdentity
			var oldestAt time.Time
			for candidate, sample := range h.connections {
				if oldestAt.IsZero() || sample.At.Before(oldestAt) {
					oldest, oldestAt = candidate, sample.At
				}
			}
			delete(h.connections, oldest)
			h.connectionReplacements++
		}
		h.connections[key] = ServiceObservation{Namespace: key.namespace, Service: key.service, Environment: environment,
			Version: attr("service.version"), RunID: attr("geko.run.id"), TraceID: hex.EncodeToString(span.TraceID[:]), At: at}
	}
}

// Connections returns a bounded copy. Old retained database traces and imports
// cannot masquerade as live OTLP arrivals; this cache resets with the receiver.
func (h *Receiver) Connections() ConnectionSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := ConnectionSnapshot{Replacements: h.connectionReplacements}
	cutoff := time.Now().Add(-connectionHistory)
	for key, sample := range h.connections {
		if sample.At.Before(cutoff) {
			delete(h.connections, key)
			continue
		}
		result.Services = append(result.Services, sample)
	}
	slices.SortFunc(result.Services, func(a, b ServiceObservation) int { return b.At.Compare(a.At) })
	return result
}
