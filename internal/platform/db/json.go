package db

import "encoding/json"

// JSONOrNil JSON-encodes v for a nullable jsonb column: a nil v stores
// NULL. Marshal errors are returned so a bad value fails the write rather
// than silently storing NULL.
func JSONOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
