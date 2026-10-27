package memory

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"projectmap/internal/jsonutil"
	"strings"
	"time"
)

type Cursor struct {
	Scope        string `json:"scope"`
	Revision     int64  `json:"revision"`
	Time         string `json:"time,omitempty"`
	ID           string `json:"id,omitempty"`
	LastRevision int64  `json:"last_revision,omitempty"`
}
type Cursors struct{ secret [32]byte }

func NewCursors() *Cursors { out := &Cursors{}; _, _ = rand.Read(out.secret[:]); return out }
func Scope(profile string, query Query) string {
	query.Cursor = ""
	body, _ := json.Marshal(struct {
		Profile string
		Query   Query
	}{profile, query})
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}
func (c *Cursors) Encode(cursor Cursor) string {
	body, _ := json.Marshal(cursor)
	signature := hmac.New(sha256.New, c.secret[:])
	_, _ = signature.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
}
func (c *Cursors) Decode(token, scope string) (Cursor, error) {
	fail := func() (Cursor, error) {
		return Cursor{}, Problem(400, "invalid_cursor", "Invalid or mismatched cursor.")
	}
	if len(token) > 2048 {
		return fail()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return fail()
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fail()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fail()
	}
	mac := hmac.New(sha256.New, c.secret[:])
	_, _ = mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return fail()
	}
	var cursor Cursor
	if jsonutil.Decode(body, &cursor) != nil || cursor.Scope != scope || cursor.Revision < 0 {
		return fail()
	}
	if cursor.LastRevision > 0 {
		if cursor.Time != "" || cursor.ID != "" {
			return fail()
		}
	} else {
		if !ValidID(cursor.ID) {
			return fail()
		}
		if _, err := time.Parse(time.RFC3339Nano, cursor.Time); err != nil {
			return fail()
		}
	}
	return cursor, nil
}
func CheckCursorRevision(cursor Cursor, revision int64) error {
	if cursor.Revision != revision {
		return &Error{Status: 409, Code: "cursor_stale", Message: "Project changed; restart the listing.", Details: Record{"current_revision": revision}}
	}
	return nil
}
