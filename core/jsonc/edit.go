package jsonc

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

// Set returns data with the value at path set, or removed when value is
// nil. Path elements are object keys, or indexes into arrays. Missing
// objects along the path are created. Comments are kept, and the result
// is formatted (see hujson.Format).
func Set(data []byte, path []string, value any) ([]byte, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{\n}\n")
	}
	v, err := hujson.Parse(prepare(data))
	if err != nil {
		return nil, err
	}
	var ops []string
	for i := 1; i < len(path); i++ {
		if v.Find(Pointer(path[:i]...)) == nil {
			if value == nil {
				return data, nil // nothing to remove
			}
			ops = append(ops, fmt.Sprintf(`{"op": "add", "path": %s, "value": {%s}}`, quote(Pointer(path[:i]...)), "\n"))
		}
	}
	ptr := Pointer(path...)
	switch {
	case value == nil && v.Find(ptr) == nil:
		return data, nil
	case value == nil:
		ops = append(ops, fmt.Sprintf(`{"op": "remove", "path": %s}`, quote(ptr)))
	default:
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		ops = append(ops, fmt.Sprintf(`{"op": "add", "path": %s, "value": %s}`, quote(ptr), b))
	}
	if err := v.Patch([]byte("[" + strings.Join(ops, ",") + "]")); err != nil {
		return nil, err
	}
	v.Format()
	return v.Pack(), nil
}

// Pointer returns the JSON pointer (RFC 6901) for a key path.
func Pointer(path ...string) string {
	var b strings.Builder
	for _, p := range path {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(p))
	}
	return b.String()
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
