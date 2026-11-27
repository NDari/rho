// Package jsonc reads and edits Rho's JSONC files (JSON with comments and
// trailing commas): the config, themes and extension manifests.
package jsonc

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
)

// Meta describes the keys a decoded file set.
type Meta struct {
	tree    any
	unknown []string
}

// Decode parses JSONC data into v, which must point to a struct. Object
// keys match the fields' json tags exactly (case included). A file that is
// empty or only whitespace decodes as {}.
func Decode(data []byte, v any) (Meta, error) {
	std, err := Standardize(data)
	if err != nil {
		return Meta{}, err
	}
	var tree any
	if err := json.Unmarshal(std, &tree); err != nil {
		return Meta{}, err
	}
	if _, ok := tree.(map[string]any); !ok {
		return Meta{}, fmt.Errorf("the file must hold a JSON object")
	}
	if err := json.Unmarshal(std, v); err != nil {
		return Meta{}, err
	}
	var unknown []string
	walk(reflect.TypeOf(v), tree, nil, &unknown)
	slices.Sort(unknown)
	return Meta{tree: tree, unknown: slices.Compact(unknown)}, nil
}

// Standardize turns JSONC into plain JSON, keeping byte offsets.
func Standardize(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return []byte("{}"), nil
	}
	return hujson.Standardize(prepare(data))
}

// prepare copies data for hujson, which works in place, and ends it with a
// newline: hujson rejects a line comment that ends the file without one.
func prepare(data []byte) []byte {
	out := bytes.Clone(data)
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return out
}

// IsDefined reports whether the file set the key path (for example
// "bash", "timeout"), even to null.
func (m Meta) IsDefined(key ...string) bool {
	cur := m.tree
	for _, k := range key {
		obj, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = obj[k]; !ok {
			return false
		}
	}
	return true
}

// Keys returns the keys of the object at the key path, sorted, or nil
// when the file set no object there.
func (m Meta) Keys(key ...string) []string {
	cur := m.tree
	for _, k := range key {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[k]
	}
	obj, ok := cur.(map[string]any)
	if !ok {
		return nil
	}
	return sortedKeys(obj)
}

// Undecoded returns the keys that match no field, as dotted paths
// ("tui.colour"), in a stable order.
func (m Meta) Undecoded() []string { return m.unknown }

// walk reports the keys of val that typ has no field for.
func walk(typ reflect.Type, val any, path []string, unknown *[]string) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := val.(map[string]any)
		if !ok {
			return
		}
		fields := map[string]reflect.Type{}
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name != "" && name != "-" && f.IsExported() {
				fields[name] = f.Type
			}
		}
		for _, k := range sortedKeys(obj) {
			p := append(slices.Clip(path), k)
			ft, ok := fields[k]
			if !ok {
				*unknown = append(*unknown, strings.Join(p, "."))
				continue
			}
			walk(ft, obj[k], p, unknown)
		}
	case reflect.Map:
		obj, ok := val.(map[string]any)
		if !ok {
			return
		}
		for _, k := range sortedKeys(obj) {
			walk(typ.Elem(), obj[k], append(slices.Clip(path), k), unknown)
		}
	case reflect.Slice, reflect.Array:
		arr, ok := val.([]any)
		if !ok {
			return
		}
		for _, e := range arr {
			walk(typ.Elem(), e, path, unknown)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
