package main

import "testing"

func TestProtocolSelection(t *testing.T) {
	for _, tc := range []struct {
		name, general, traces, want string
		invalid                     bool
	}{
		{name: "default", want: "http/protobuf"},
		{name: "grpc", general: "grpc", want: "grpc"},
		{name: "signal override", general: "http/protobuf", traces: "grpc", want: "grpc"},
		{name: "signal protobuf", general: "grpc", traces: "http/protobuf", want: "http/protobuf"},
		{name: "unsupported", general: "http/json", invalid: true},
		{name: "invalid signal does not fall back", general: "grpc", traces: "http/json", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", tc.general)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", tc.traces)
			got, err := selectedProtocol()
			if tc.invalid {
				if err == nil {
					t.Fatal("unsupported exporter protocol accepted")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("protocol=%q, err=%v; want %q", got, err, tc.want)
			}
		})
	}
}
