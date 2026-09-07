package harness

import (
	"encoding/json"
	"fmt"
)

// MarkerField names the scenario that published a message.
//
// Give every message the suite publishes this extra field: most contracts say
// unknown fields are ignored, so it changes nothing the service does, and
// because services typically log the raw payload it gives each scenario one
// unambiguous line to wait for even when two scenarios publish otherwise
// identical messages. Where the payload has no room for one — a protobuf
// message, a schema-registry-validated record — the adapters put it in the
// envelope instead (a Kafka header, gRPC metadata).
//
// Check the contract does say unknown fields are ignored. If it forbids them,
// vary a harmless field per scenario and match on that.
const MarkerField = "e2e_case"

// encodePayload turns a scenario's body into bytes. Passing []byte or string
// through untouched is what lets malformed-input scenarios send bytes the
// service's decoder cannot parse.
func encodePayload(body any) ([]byte, error) {
	switch b := body.(type) {
	case nil:
		return nil, nil
	case []byte:
		return b, nil
	case string:
		return []byte(b), nil
	default:
		encoded, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("encoding payload: %w", err)
		}
		return encoded, nil
	}
}
