package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/NDari/rho/core/jsonc"
)

// FileView is what one config file sets, for a settings form.
type FileView struct {
	Path    string `json:"path"`
	Project bool   `json:"project,omitempty"`
	Exists  bool   `json:"exists"`
	// Version identifies the file's contents; an edit made from an older
	// version is refused (see EditFile).
	Version string `json:"version"`
	// Values is the file's content as JSON values.
	Values map[string]any `json:"values"`
	// Error is set when the file can't be read or parsed.
	Error string `json:"error,omitempty"`
}

// View is the config as a settings form shows it.
type View struct {
	Global FileView `json:"global"`
	// Project is nil when project files are ignored or there is no
	// project directory apart from the Rho home.
	Project *FileView `json:"project,omitempty"`
	// Effective is the merged config as JSON values, each provider's
	// models (with defaults filled in) under it. Unset optional values are
	// null; Field.Default gives them.
	Effective map[string]any `json:"effective"`
	// Defaults is the built-in config as JSON values: what applies when
	// neither file sets a key.
	Defaults map[string]any `json:"defaults"`
	// Builtin names the built-in providers.
	Builtin  []string `json:"builtin"`
	Warnings []string `json:"warnings,omitempty"`
	// Error is set when the merged config doesn't load.
	Error string `json:"error,omitempty"`
}

// LoadView reads both config files. It works when the config doesn't
// load, so a form can fix it.
func LoadView(p Paths) View {
	v := View{Global: readView(p.GlobalFile(), false)}
	if f := p.ProjectFile(); f != "" {
		pv := readView(f, true)
		v.Project = &pv
	}
	cfg, warnings, err := load(p, os.ReadFile)
	if err == nil {
		err = cfg.check()
	}
	v.Warnings = warnings
	if err != nil {
		v.Error = err.Error()
	}
	v.Effective = cfg.tree()
	v.Defaults = Defaults().tree()
	v.Builtin = slices.Sorted(maps.Keys(Defaults().Providers))
	return v
}

func readView(path string, project bool) FileView {
	v := FileView{Path: path, Project: project, Values: map[string]any{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return v
	}
	v.Exists = true
	v.Version = fileVersion(data)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	tree, err := decodeTree(data)
	if err != nil {
		v.Error = fmt.Sprintf("parse %s: %v", path, err)
		return v
	}
	v.Values = tree
	return v
}

func fileVersion(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func decodeTree(data []byte) (map[string]any, error) {
	std, err := jsonc.Standardize(data)
	if err != nil {
		return nil, err
	}
	var tree map[string]any
	if err := json.Unmarshal(std, &tree); err != nil {
		return nil, err
	}
	if tree == nil {
		tree = map[string]any{}
	}
	return tree, nil
}

// tree returns c as JSON values, with each provider's models resolved
// (defaults filled in, tool options inherited) under it.
func (c Config) tree() map[string]any {
	var out map[string]any
	if b, err := json.Marshal(c); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	provs, _ := out["providers"].(map[string]any)
	for name, pv := range provs {
		obj, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		models := []any{}
		for _, m := range c.Models {
			if m.Provider != name {
				continue
			}
			var mv any
			if b, err := json.Marshal(m.withDefaults().inherit(c.Providers[name])); err == nil {
				_ = json.Unmarshal(b, &mv)
			}
			models = append(models, mv)
		}
		obj["models"] = models
	}
	return out
}

// check reports problems Validate leaves to the start of Rho: a default
// model that isn't configured, or no model at all.
func (c Config) check() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.DefaultModel != "" {
		if _, err := c.ResolveModel(c.DefaultModel); err != nil {
			return fmt.Errorf("default_model %q is not a configured model", c.DefaultModel)
		}
	}
	if _, err := c.ResolveModel(""); err != nil {
		return err
	}
	return nil
}

// Edit kinds.
const (
	// EditSet sets the top-level field Key to Value (nil removes it).
	EditSet = "set"
	// EditProvider saves provider Provider ("" for a new one) as Name
	// with Fields.
	EditProvider = "provider"
	// EditDeleteProvider removes provider Provider and its models.
	EditDeleteProvider = "delete_provider"
	// EditModel saves the model at Index (-1 for a new one) of provider
	// Provider with Fields, under provider Name (when it differs, the
	// model moves there).
	EditModel = "model"
	// EditDeleteModel removes the model at Index of provider Provider.
	EditDeleteModel = "delete_model"
)

// Edit is one change a settings form makes to a config file.
type Edit struct {
	Kind     string `json:"kind"`
	Key      string `json:"key,omitempty"`
	Value    any    `json:"value,omitempty"`
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
	Index    int    `json:"index,omitempty"`
	// Fields are the provider's or model's keys from ProviderFields or
	// ModelFields; a missing or null key is unset. Keys the forms don't
	// know are left as they are.
	Fields map[string]any `json:"fields,omitempty"`
}

// ErrFileChanged is returned when the file changed since the form read it.
var ErrFileChanged = errors.New("the config file changed since it was read; reload it and try again")

// EditFile makes the edits to the global or project config file and saves
// it, keeping its comments. version is the FileView.Version the edits were
// made from. A change that would stop the config from loading, or that
// Rho would ignore (such as a provider setting in a project file), is
// refused and nothing is written.
func EditFile(p Paths, project bool, version string, edits []Edit) error {
	path := p.GlobalFile()
	if project {
		if path = p.ProjectFile(); path == "" {
			return errors.New("there is no project config file to edit")
		}
	}
	before, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if (exists && version != fileVersion(before)) || (!exists && version != "") {
		return ErrFileChanged
	}
	tree, err := decodeTree(before)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	var ops []jsonc.Op
	for _, e := range edits {
		o, err := e.ops(tree, project)
		if err != nil {
			return err
		}
		ops = append(ops, o...)
	}
	after, err := jsonc.Apply(before, ops...)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	if err := checkChange(p, path, before, after, exists); err != nil {
		return err
	}
	return writeFile(path, after)
}

// checkChange refuses after when it brings problems before didn't have:
// errors that stop the config from loading, or ignored settings.
func checkChange(p Paths, path string, before, after []byte, existed bool) error {
	run := func(data []byte, present bool) ([]string, []string) {
		cfg, warnings, err := load(p, func(f string) ([]byte, error) {
			if f == path {
				if !present {
					return nil, fs.ErrNotExist
				}
				return data, nil
			}
			return os.ReadFile(f)
		})
		if err == nil {
			err = cfg.check()
		}
		var errs []string
		if err != nil {
			errs = strings.Split(err.Error(), "\n")
		}
		return errs, warnings
	}
	oldErrs, oldWarnings := run(before, existed)
	newErrs, newWarnings := run(after, true)
	var problems []string
	for _, e := range newErrs {
		if !slices.Contains(oldErrs, e) {
			problems = append(problems, strings.TrimPrefix(e, path+": "))
		}
	}
	for _, w := range newWarnings {
		if !slices.Contains(oldWarnings, w) {
			problems = append(problems, strings.TrimPrefix(w, path+": "))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	return nil
}

func (e Edit) ops(tree map[string]any, project bool) ([]jsonc.Op, error) {
	switch e.Kind {
	case EditSet:
		i := slices.IndexFunc(FileFields, func(f Field) bool { return f.Key == e.Key })
		if i < 0 {
			return nil, fmt.Errorf("unknown setting %q", e.Key)
		}
		f := FileFields[i]
		if project && f.GlobalOnly {
			return nil, fmt.Errorf("%s can only be set in the global config", e.Key)
		}
		v, err := normalize(f, e.Value)
		if err != nil {
			return nil, err
		}
		return []jsonc.Op{{Path: strings.Split(e.Key, "."), Value: v}}, nil
	case EditProvider:
		return providerOps(tree, e)
	case EditDeleteProvider:
		if _, ok := child(tree, "providers", e.Provider).(map[string]any); !ok {
			return nil, fmt.Errorf("provider %q is not in the file", e.Provider)
		}
		return []jsonc.Op{{Path: []string{"providers", e.Provider}}}, nil
	case EditModel:
		return modelOps(tree, e)
	case EditDeleteModel:
		if _, ok := modelAt(tree, e.Provider, e.Index); !ok {
			return nil, fmt.Errorf("provider %q has no model %d in the file", e.Provider, e.Index)
		}
		return []jsonc.Op{{Path: []string{"providers", e.Provider, "models", strconv.Itoa(e.Index)}}}, nil
	}
	return nil, fmt.Errorf("unknown edit %q", e.Kind)
}

func providerOps(tree map[string]any, e Edit) ([]jsonc.Op, error) {
	name := strings.TrimSpace(e.Name)
	if name == "" {
		return nil, errors.New("the provider needs a name")
	}
	if strings.ContainsAny(name, "/") {
		return nil, errors.New("a provider name can't contain /")
	}
	cur, exists := child(tree, "providers", e.Provider).(map[string]any)
	if e.Provider == "" {
		exists = false
	}
	var ops []jsonc.Op
	if name != e.Provider {
		if _, taken := child(tree, "providers", name).(map[string]any); taken {
			return nil, fmt.Errorf("there is already a provider named %q", name)
		}
		if exists {
			ops = append(ops, jsonc.Op{From: []string{"providers", e.Provider}, Path: []string{"providers", name}})
		}
	}
	if !exists {
		cur = map[string]any{}
	}
	diff, err := diffOps([]string{"providers", name}, cur, e.Fields, ProviderFields)
	if err != nil {
		return nil, err
	}
	if !exists && len(diff) == 0 {
		// An entry with no settings of its own, e.g. a built-in provider
		// that only gets models later.
		return append(ops, jsonc.Op{Path: []string{"providers", name}, Value: map[string]any{}}), nil
	}
	return append(ops, diff...), nil
}

func modelOps(tree map[string]any, e Edit) ([]jsonc.Op, error) {
	target := e.Name
	if target == "" {
		target = e.Provider
	}
	if target == "" {
		return nil, errors.New("the model needs a provider")
	}
	id, _ := e.Fields["id"].(string)
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("the model needs an ID")
	}
	var ops []jsonc.Op
	if e.Index < 0 {
		// Add the keys one by one, so they are in the forms' order.
		list, _ := child(tree, "providers", target, "models").([]any)
		ops = append(ops, jsonc.Op{Path: []string{"providers", target, "models", "-"}, Value: map[string]any{}})
		diff, err := diffOps([]string{"providers", target, "models", strconv.Itoa(len(list))}, nil, e.Fields, ModelFields)
		return append(ops, diff...), err
	}
	cur, ok := modelAt(tree, e.Provider, e.Index)
	if !ok {
		return nil, fmt.Errorf("provider %q has no model %d in the file", e.Provider, e.Index)
	}
	diff, err := diffOps([]string{"providers", e.Provider, "models", strconv.Itoa(e.Index)}, cur, e.Fields, ModelFields)
	if err != nil {
		return nil, err
	}
	ops = append(ops, diff...)
	if target != e.Provider {
		ops = append(ops, jsonc.Op{
			From: []string{"providers", e.Provider, "models", strconv.Itoa(e.Index)},
			Path: []string{"providers", target, "models", "-"},
		})
	}
	// Keep default_model pointing at the model when its ID changes.
	if old, _ := cur["id"].(string); old != id && tree["default_model"] == old {
		ops = append(ops, jsonc.Op{Path: []string{"default_model"}, Value: id})
	}
	return ops, nil
}

// diffOps returns the changes that make the object cur at base hold want,
// for the keys in fields. Unchanged keys are left alone, so their
// comments stay.
func diffOps(base []string, cur, want map[string]any, fields []Field) ([]jsonc.Op, error) {
	var ops []jsonc.Op
	for _, f := range fields {
		w, err := normalize(f, want[f.Key])
		if err != nil {
			return nil, err
		}
		c, had := cur[f.Key]
		path := append(slices.Clone(base), f.Key)
		switch {
		case w == nil && had:
			ops = append(ops, jsonc.Op{Path: path})
		case w != nil && (!had || !sameJSON(c, w)):
			ops = append(ops, jsonc.Op{Path: path, Value: w})
		}
	}
	return ops, nil
}

// normalize checks that v suits f, returning nil for an unset value.
func normalize(f Field, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	bad := func(want string) error { return fmt.Errorf("%s must be %s", f.Label, want) }
	switch f.Kind {
	case "string", "choice":
		s, ok := v.(string)
		if !ok {
			return nil, bad("text")
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, nil
		}
		if f.Kind == "choice" && len(f.Choices) > 0 && !slices.Contains(f.Choices, s) {
			return nil, bad("one of " + strings.Join(f.Choices, ", "))
		}
		return s, nil
	case "int":
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) {
			return nil, bad("a whole number")
		}
		return int64(n), nil
	case "number":
		if _, ok := v.(float64); !ok {
			return nil, bad("a number")
		}
		return v, nil
	case "bool":
		if _, ok := v.(bool); !ok {
			return nil, bad("true or false")
		}
		return v, nil
	case "list":
		list, ok := v.([]any)
		if !ok {
			return nil, bad("a list")
		}
		out := []string{}
		for _, e := range list {
			s, ok := e.(string)
			if !ok {
				return nil, bad("a list of names")
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case "headers":
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, bad("header names and values")
		}
		out := map[string]string{}
		for k, e := range obj {
			s, ok := e.(string)
			if !ok {
				return nil, bad("header names and values")
			}
			if k = strings.TrimSpace(k); k != "" {
				out[k] = s
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	case "pricing":
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, bad("prices")
		}
		var out prices
		for k, dst := range map[string]**float64{"input": &out.Input, "output": &out.Output, "cache_read": &out.CacheRead, "cache_write": &out.CacheWrite} {
			switch n := obj[k].(type) {
			case nil:
			case float64:
				if n < 0 {
					return nil, bad("prices of 0 or more")
				}
				*dst = &n
			default:
				return nil, bad("prices")
			}
		}
		if out == (prices{}) {
			return nil, nil
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: unknown kind %q", f.Key, f.Kind)
}

// prices is a model's pricing as written to the file, in this order.
type prices struct {
	Input      *float64 `json:"input,omitzero"`
	Output     *float64 `json:"output,omitzero"`
	CacheRead  *float64 `json:"cache_read,omitzero"`
	CacheWrite *float64 `json:"cache_write,omitzero"`
}

func sameJSON(a, b any) bool {
	ja, err1 := json.Marshal(a, json.Deterministic(true))
	jb, err2 := json.Marshal(b, json.Deterministic(true))
	if err1 != nil || err2 != nil {
		return false
	}
	// Normalize numbers and key order through a round trip.
	var va, vb any
	if json.Unmarshal(ja, &va) != nil || json.Unmarshal(jb, &vb) != nil {
		return false
	}
	ja, _ = json.Marshal(va, json.Deterministic(true))
	jb, _ = json.Marshal(vb, json.Deterministic(true))
	return string(ja) == string(jb)
}

func child(tree map[string]any, key ...string) any {
	var cur any = tree
	for _, k := range key {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[k]
	}
	return cur
}

func modelAt(tree map[string]any, provider string, i int) (map[string]any, bool) {
	list, _ := child(tree, "providers", provider, "models").([]any)
	if i < 0 || i >= len(list) {
		return nil, false
	}
	m, ok := list[i].(map[string]any)
	return m, ok
}

// ProviderFrom returns the provider name would be with fields (keys of
// ProviderFields) set over the built-in provider of that name, if any.
// It is for trying a provider's settings before they are saved.
func ProviderFrom(name string, fields map[string]any) (Provider, error) {
	p, ok := Defaults().Providers[name]
	if !ok {
		p = Provider{Type: ProviderOpenAICompat}
	}
	set := map[string]any{}
	for _, f := range ProviderFields {
		v, err := normalize(f, fields[f.Key])
		if err != nil {
			return p, err
		}
		if v != nil {
			set[f.Key] = v
		}
	}
	b, err := json.Marshal(set)
	if err != nil {
		return p, err
	}
	var over Provider
	if err := json.Unmarshal(b, &over); err != nil {
		return p, err
	}
	return mergeProvider(p, over, func(k string) bool { _, ok := set[k]; return ok }), nil
}
