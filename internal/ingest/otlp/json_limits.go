package otlp

import (
	"encoding/json"
	"fmt"
	"io"
)

// Check tokens before allocating RawMessage arrays. Unknown fields and nested
// AnyValue arrays also consume the budget so they cannot bypass the limits.
func checkJSONBatch(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	items, tokens := 0, 0
	envelopes := map[string]int{}
	var value func(string, int) error
	value = func(field string, depth int) error {
		if depth > maxWireDepth {
			return errBatchLimit
		}
		tokens++
		if tokens > maxDecodedMessages*8 {
			return errBatchLimit
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("invalid JSON object key")
				}
				if err := value(name, depth+1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case json.Delim('['):
			for decoder.More() {
				items++
				if items > maxDecodedMessages {
					return errBatchLimit
				}
				name := field
				switch field {
				case "resource_spans":
					name = "resourceSpans"
				case "scope_spans":
					name = "scopeSpans"
				}
				switch name {
				case "resourceSpans", "scopeSpans", "spans":
					envelopes[name]++
					if envelopes[name] > maxSpans {
						return errBatchLimit
					}
				}
				if err := value("", depth+1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := value("", 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}
