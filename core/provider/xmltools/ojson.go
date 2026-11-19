package xmltools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// object is a JSON object that keeps its key order, so schemas render in a
// stable order (which keeps prompts cacheable) and arguments replay as the
// model wrote them.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(k string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[k]
	return v, ok
}

// set adds or replaces a key, keeping the first position of a key.
func (o *object) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// MarshalJSON implements json.Marshaler.
func (o *object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal encodes v without escaping <, > and & (tool arguments are code).
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// decodeOrdered parses JSON, returning *object for objects, []any for
// arrays, json.Number for numbers, and string, bool or nil otherwise.
func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, errors.New("unexpected delimiter")
	default:
		return t, nil
	}
}

// Schema helpers. Schemas are decoded with decodeOrdered.

func asObject(v any) *object {
	o, _ := v.(*object)
	return o
}

func str(o *object, k string) string {
	v, _ := o.get(k)
	s, _ := v.(string)
	return s
}

// properties returns a schema's properties in order.
func properties(schema *object) []field {
	props := asObject(mustGet(schema, "properties"))
	if props == nil {
		return nil
	}
	out := make([]field, 0, len(props.keys))
	for _, k := range props.keys {
		out = append(out, field{k, asObject(props.vals[k])})
	}
	return out
}

func property(schema *object, name string) *object {
	return asObject(mustGet(asObject(mustGet(schema, "properties")), name))
}

func required(schema *object) map[string]bool {
	out := map[string]bool{}
	arr, _ := mustGet(schema, "required").([]any)
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out[s] = true
		}
	}
	return out
}

func mustGet(o *object, k string) any {
	v, _ := o.get(k)
	return v
}

type field struct {
	name   string
	schema *object
}

func variants(schema *object) []any {
	if v, ok := mustGet(schema, "anyOf").([]any); ok {
		return v
	}
	v, _ := mustGet(schema, "oneOf").([]any)
	return v
}

// schemaTypes lists the JSON types a schema allows.
func schemaTypes(schema *object) []string {
	if schema == nil {
		return nil
	}
	switch t := mustGet(schema, "type").(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, v := range t {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if vs := variants(schema); vs != nil {
		var out []string
		for _, v := range vs {
			out = append(out, schemaTypes(asObject(v))...)
		}
		return out
	}
	sample, ok := schema.get("const")
	if enum, isArr := mustGet(schema, "enum").([]any); isArr && len(enum) > 0 {
		sample, ok = enum[0], true
	}
	if ok {
		switch sample.(type) {
		case json.Number:
			return []string{"number"}
		case bool:
			return []string{"boolean"}
		default:
			return []string{"string"}
		}
	}
	if _, ok := schema.get("properties"); ok {
		return []string{"object"}
	}
	if _, ok := schema.get("items"); ok {
		return []string{"array"}
	}
	return nil
}

// pickVariant returns the anyOf/oneOf variant that allows typ, or schema.
func pickVariant(schema *object, typ string) *object {
	for _, v := range variants(schema) {
		vo := asObject(v)
		for _, t := range schemaTypes(vo) {
			if t == typ {
				return vo
			}
		}
	}
	return schema
}
