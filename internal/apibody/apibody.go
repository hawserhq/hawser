// Package apibody reads Docker API request bodies the way dockerd reads them.
//
// It exists because a policy gate that inspects a decoded map[string]any does
// NOT see what the daemon will act on, and the difference is a bypass.
//
// Go's encoding/json matches object keys to struct fields case-insensitively,
// and when a document repeats a key the last occurrence wins. dockerd decodes
// into typed structs, so it honours `{"image": ...}` and `{"IMAGE": ...}` just
// as it honours `{"Image": ...}`. A gate doing body["Image"] sees none of them,
// and a gate reading `{"Image":"allowed","image":"blocked"}` sees the allowed
// one while the daemon runs the blocked one.
//
// Two rules, and both are needed:
//
//  1. Ambiguous refuses a document that spells one field two ways. Replicating
//     "last wins" is impossible once a map has been decoded -- document order
//     is gone -- and guessing at a security boundary is how bypasses ship. No
//     real client sends such a document.
//  2. The accessors below fold case, so a single lower- or upper-case spelling
//     is still judged.
//
// What this package deliberately does NOT do is rewrite keys. Several Docker
// API objects are keyed by DATA rather than by field name -- Labels, Volumes,
// ExposedPorts, PortBindings -- and case-folding those would corrupt a
// container's configuration (a Volumes entry for "/Data" is not "/data").
package apibody

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Field returns the value of the named field, matching case-insensitively the
// way encoding/json does. An exact hit wins without scanning.
func Field(m map[string]any, name string) (any, bool) {
	if m == nil {
		return nil, false
	}
	if v, ok := m[name]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// String returns a string field, or "" when absent or another type.
func String(m map[string]any, name string) string {
	v, _ := Field(m, name)
	s, _ := v.(string)
	return s
}

// Map returns a nested object field.
func Map(m map[string]any, name string) (map[string]any, bool) {
	v, ok := Field(m, name)
	if !ok {
		return nil, false
	}
	sub, ok := v.(map[string]any)
	return sub, ok
}

// Slice returns an array field.
func Slice(m map[string]any, name string) ([]any, bool) {
	v, ok := Field(m, name)
	if !ok {
		return nil, false
	}
	s, ok := v.([]any)
	return s, ok
}

// SetField writes through to the key already present under any spelling, so a
// translated value replaces the one the daemon would have read rather than
// adding a second spelling beside it.
func SetField(m map[string]any, name string, value any) {
	if m == nil {
		return
	}
	if _, ok := m[name]; ok {
		m[name] = value
		return
	}
	for k := range m {
		if strings.EqualFold(k, name) {
			m[k] = value
			return
		}
	}
	m[name] = value
}

// Guarded names the fields a policy gate judges. Only these are checked for
// ambiguity.
//
// Checking every field would be wrong, not merely stricter: several Docker API
// objects are keyed by DATA, and two spellings there are two distinct, legitimate
// entries. Labels {"app":"x","App":"y"} is two labels, and refusing it would
// break real containers. A duplicate outside this set cannot bypass anything,
// because nothing reads it.
var Guarded = []string{
	"Image",
	"HostConfig",
	"Privileged",
	"CapAdd",
	"Binds",
	"Mounts",
	"Source",
	"Type",
}

func guarded(folded string) bool {
	for _, g := range Guarded {
		if folded == strings.ToLower(g) {
			return true
		}
	}
	return false
}

// Ambiguous reports whether raw JSON spells any GUARDED field more than one
// way, at any depth, and names the first such field.
//
// Streamed with a token decoder rather than checked on a decoded map, because
// decoding is exactly what destroys the evidence: `{"a":1,"A":2}` becomes two
// distinct map keys with no record of which came last, and `{"a":1,"a":2}`
// becomes one.
func Ambiguous(raw []byte) (field string, ambiguous bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	f, err := scan(dec)
	if err != nil {
		// Malformed JSON is not this function's business; the caller's own
		// decode reports it, and dockerd would reject it too.
		return "", false
	}
	return f, f != ""
}

// scan walks one JSON value, returning the first duplicated field name.
func scan(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return "", nil // a scalar
	}

	switch delim {
	case '{':
		// Case-folded name -> the spelling already seen, so the error can show
		// both spellings a reader has to look for.
		seen := map[string]string{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return "", err
			}
			key, _ := kt.(string)
			folded := strings.ToLower(key)
			if guarded(folded) {
				if prev, dup := seen[folded]; dup {
					return fmt.Sprintf("%s/%s", prev, key), nil
				}
				seen[folded] = key
			}
			if f, err := scan(dec); err != nil {
				return "", err
			} else if f != "" {
				return f, nil
			}
		}
	case '[':
		for dec.More() {
			if f, err := scan(dec); err != nil {
				return "", err
			} else if f != "" {
				return f, nil
			}
		}
	}

	// Consume the closing delimiter.
	if _, err := dec.Token(); err != nil && err != io.EOF {
		return "", err
	}
	return "", nil
}
