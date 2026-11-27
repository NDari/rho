package config

import (
	"slices"

	"github.com/tailscale/hujson"
)

// nestModels moves the models of a converted config.toml, a top-level
// list in which each model names its provider, under their providers
// (providers.NAME.models). Without default_model, it sets it to the first
// model, which the old format used when none was set. A model that names
// no provider is left in place, so the loader warns about it.
func nestModels(data []byte) ([]byte, error) {
	root, err := hujson.Parse(data)
	if err != nil {
		return nil, err
	}
	obj, ok := root.Value.(*hujson.Object)
	if !ok {
		return data, nil
	}
	list, ok := memberValue(obj, "models").(*hujson.Array)
	if !ok {
		return data, nil
	}
	var keep []hujson.ArrayElement
	first := ""
	for _, e := range list.Elements {
		m, _ := e.Value.(*hujson.Object)
		prov := stringMember(m, "provider")
		if prov == "" {
			keep = append(keep, e)
			continue
		}
		models := array(object(object(obj, "providers"), prov), "models")
		if models == nil {
			keep = append(keep, e)
			continue
		}
		if first == "" {
			first = stringMember(m, "id")
		}
		m.Members = slices.DeleteFunc(m.Members, func(om hujson.ObjectMember) bool { return name(om) == "provider" })
		models.Elements = append(models.Elements, e)
	}
	if len(keep) > 0 {
		list.Elements = keep
	} else {
		obj.Members = slices.DeleteFunc(obj.Members, func(om hujson.ObjectMember) bool { return name(om) == "models" })
	}
	if memberValue(obj, "default_model") == nil && first != "" {
		obj.Members = slices.Insert(obj.Members, 0, hujson.ObjectMember{
			Name:  hujson.Value{Value: hujson.String("default_model")},
			Value: hujson.Value{Value: hujson.String(first)},
		})
	}
	root.Format()
	return root.Pack(), nil
}

func name(om hujson.ObjectMember) string {
	if lit, ok := om.Name.Value.(hujson.Literal); ok {
		return lit.String()
	}
	return ""
}

// memberValue returns the value of obj's member key, or nil.
func memberValue(obj *hujson.Object, key string) hujson.ValueTrimmed {
	if obj == nil {
		return nil
	}
	for _, om := range obj.Members {
		if name(om) == key {
			return om.Value.Value
		}
	}
	return nil
}

// stringMember returns obj's member key when it is a string, or "".
func stringMember(obj *hujson.Object, key string) string {
	if lit, ok := memberValue(obj, key).(hujson.Literal); ok && lit.Kind() == '"' {
		return lit.String()
	}
	return ""
}

// object returns obj's member key, adding an empty object when there is
// none, or nil when it is not an object.
func object(obj *hujson.Object, key string) *hujson.Object {
	return composite[*hujson.Object](obj, key, func() hujson.ValueTrimmed { return &hujson.Object{AfterExtra: hujson.Extra("\n")} })
}

// array is object for an array.
func array(obj *hujson.Object, key string) *hujson.Array {
	return composite[*hujson.Array](obj, key, func() hujson.ValueTrimmed { return &hujson.Array{AfterExtra: hujson.Extra("\n")} })
}

func composite[T hujson.ValueTrimmed](obj *hujson.Object, key string, empty func() hujson.ValueTrimmed) T {
	var zero T
	if obj == nil || key == "" {
		return zero
	}
	v := memberValue(obj, key)
	if v == nil {
		v = empty()
		obj.Members = append(obj.Members, hujson.ObjectMember{
			Name:  hujson.Value{Value: hujson.String(key)},
			Value: hujson.Value{Value: v},
		})
	}
	t, _ := v.(T)
	return t
}
