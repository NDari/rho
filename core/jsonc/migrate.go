package jsonc

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/tailscale/hujson"
)

// ConvertTOML writes the TOML file src as JSONC to dst, when src exists
// and dst does not. Comments in src are not carried over. fix, if not nil,
// changes the converted JSONC before it is written. It reports whether it
// wrote dst; src is left in place.
//
// Rho used TOML files before it switched to JSONC; this lets existing
// files keep working.
func ConvertTOML(src, dst string, fix func([]byte) ([]byte, error)) (bool, error) {
	data, err := os.ReadFile(src)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	}
	out, err := FromTOML(data)
	if err == nil && fix != nil {
		out, err = fix(out)
	}
	if err != nil {
		return false, fmt.Errorf("convert %s: %w", src, err)
	}
	header := "// Converted from " + filepath.Base(src) + ", which Rho no longer reads.\n" +
		"// Comments in that file were not carried over.\n"
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fi, err := os.Stat(src); err == nil {
		_ = f.Chmod(fi.Mode().Perm())
	}
	if _, err := f.WriteString(header + string(out)); err != nil {
		_ = f.Close()
		_ = os.Remove(dst)
		return false, err
	}
	return true, f.Close()
}

// FromTOML converts a TOML document to formatted JSONC, keeping the order
// of its keys.
func FromTOML(data []byte) ([]byte, error) {
	var m map[string]any
	md, err := toml.Decode(string(data), &m)
	if err != nil {
		return nil, err
	}
	order := map[string]int{}
	for i, k := range md.Keys() {
		// A table's position is that of its first key: [a.b] lists only
		// "a.b" and its keys, not "a".
		for n := 1; n <= len(k); n++ {
			p := strings.Join(k[:n], "\x00")
			if _, ok := order[p]; !ok {
				order[p] = i
			}
		}
	}
	var b bytes.Buffer
	if err := writeValue(&b, m, nil, order); err != nil {
		return nil, err
	}
	v, err := hujson.Parse(b.Bytes())
	if err != nil {
		return nil, err
	}
	v.Format()
	out := v.Pack()
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, nil
}

// writeValue writes v as JSON with every object and array of objects on
// several lines, so that Format keeps them expanded.
func writeValue(b *bytes.Buffer, v any, path []string, order map[string]int) error {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		pos := func(k string) int {
			if i, ok := order[strings.Join(append(slices.Clip(path), k), "\x00")]; ok {
				return i
			}
			return len(order)
		}
		slices.SortStableFunc(keys, func(x, y string) int {
			if d := pos(x) - pos(y); d != 0 {
				return d
			}
			return strings.Compare(x, y)
		})
		b.WriteString("{\n")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(quote(k) + ": ")
			if err := writeValue(b, v[k], append(slices.Clip(path), k), order); err != nil {
				return err
			}
		}
		b.WriteString("\n}")
	case []map[string]any:
		b.WriteString("[\n")
		for i, e := range v {
			if i > 0 {
				b.WriteString(",\n")
			}
			if err := writeValue(b, e, path, order); err != nil {
				return err
			}
		}
		b.WriteString("\n]")
	default:
		j, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(j)
	}
	return nil
}
