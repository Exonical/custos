package db

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/platform/apperr"
)

// Page is a keyset-pagination request shared by all repositories.
// Cursor is opaque to clients; invalid cursors fail with CURSOR_INVALID.
type Page struct {
	Cursor string
	Limit  int
}

// Normalize bounds the page size: zero/negative Limit becomes def, and
// anything above maxLimit is clamped.
func (p Page) Normalize(def, maxLimit int) Page {
	if p.Limit <= 0 {
		p.Limit = def
	}
	if p.Limit > maxLimit {
		p.Limit = maxLimit
	}
	return p
}

// Keyset is the (created_at, id) position decoded from a cursor.
type Keyset struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type cursorJSON struct {
	T  time.Time `json:"t"`
	ID uuid.UUID `json:"id"`
}

// EncodeCursor renders the keyset position as an opaque base64url
// cursor. Cursors carry no authority — every page is re-authorized and
// re-scoped — so they are strictly validated but not signed.
func EncodeCursor(k Keyset) string {
	b, err := json.Marshal(cursorJSON{T: k.CreatedAt.UTC(), ID: k.ID})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses a client cursor back into a Keyset. Anything not
// produced by EncodeCursor is rejected as CURSOR_INVALID.
func DecodeCursor(raw string) (Keyset, error) {
	invalid := apperr.New(apperr.Invalid, "CURSOR_INVALID", "invalid cursor")
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Keyset{}, invalid
	}
	var c cursorJSON
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Keyset{}, invalid
	}
	if c.T.IsZero() || c.ID == uuid.Nil {
		return Keyset{}, invalid
	}
	return Keyset{CreatedAt: c.T, ID: c.ID}, nil
}
