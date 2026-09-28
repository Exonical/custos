package db

import "encoding/json"

// JSONOrNil JSON-encodes v for a nullable jsonb column: a nil v stores
// NULL. Marshal errors are discarded (callers pass plain maps/structs).
func JSONOrNil(v any) []byte {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}
