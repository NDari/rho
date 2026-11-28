package jsonc

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/tailscale/hujson"
)

// Op is one change to a JSONC document. Path elements are object keys, or
// indexes into arrays; "-" as the last element appends to an array.
type Op struct {
	// Path is where the change goes.
	Path []string
	// Value is the new value at Path. nil removes the key or element
	// (unless From is set).
	Value any
	// From, when set, moves the value at From to Path instead.
	From []string
}

// Set returns data with the value at path set, or removed when value is
// nil. Path elements are object keys, or indexes into arrays. Missing
// objects along the path are created. Comments are kept, and the result
// is formatted (see hujson.Format).
func Set(data []byte, path []string, value any) ([]byte, error) {
	return Apply(data, Op{Path: path, Value: value})
}

// Apply returns data with the changes made in order, each seeing the
// result of the ones before. Objects missing along a path are created (an
// array when the next element is "-"); removing something that is not
// there does nothing. Comments are kept, also inside a moved value, and
// the result is formatted (see hujson.Format).
func Apply(data []byte, ops ...Op) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{\n}\n")
	}
	v, err := hujson.Parse(prepare(data))
	if err != nil {
		return nil, err
	}
	changed := false
	for _, op := range ops {
		patch, err := patchFor(&v, op)
		if err != nil {
			return nil, err
		}
		if len(patch) == 0 {
			continue
		}
		if err := v.Patch([]byte("[" + strings.Join(patch, ",") + "]")); err != nil {
			return nil, fmt.Errorf("%s: %w", Pointer(op.Path...), err)
		}
		changed = true
	}
	if !changed {
		return data, nil
	}
	v.Format()
	return v.Pack(), nil
}

// patchFor returns the JSON patch operations (RFC 6902) for op, creating
// the containers its path needs.
func patchFor(v *hujson.Value, op Op) ([]string, error) {
	if len(op.Path) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	remove := op.Value == nil && op.From == nil
	ptr := Pointer(op.Path...)
	if remove {
		if v.Find(ptr) == nil {
			return nil, nil
		}
		return []string{fmt.Sprintf(`{"op": "remove", "path": %s}`, quote(ptr))}, nil
	}
	if op.From != nil && v.Find(Pointer(op.From...)) == nil {
		return nil, fmt.Errorf("%s: nothing to move", Pointer(op.From...))
	}
	var out []string
	for i := 1; i < len(op.Path); i++ {
		if v.Find(Pointer(op.Path[:i]...)) != nil {
			continue
		}
		empty := "{\n}"
		if op.Path[i] == "-" {
			empty = "[\n]"
		}
		out = append(out, fmt.Sprintf(`{"op": "add", "path": %s, "value": %s}`, quote(Pointer(op.Path[:i]...)), empty))
	}
	if op.From != nil {
		return append(out, fmt.Sprintf(`{"op": "move", "from": %s, "path": %s}`, quote(Pointer(op.From...)), quote(ptr))), nil
	}
	b, err := json.Marshal(op.Value, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	return append(out, fmt.Sprintf(`{"op": "add", "path": %s, "value": %s}`, quote(ptr), b)), nil
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
