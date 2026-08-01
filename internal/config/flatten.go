// Deterministic flattening of a decoded configuration value into dotted-path leaves.
//
// One walk serves three callers: hash.go turns leaves into a stable digest, secret.go
// picks the SecretRef leaves out of them, and env.go derives the set of environment-
// overridable keys by walking a zero Config. Sharing the walk means a new schema field
// is hashed, resolved and overridable without three separate edits.
//
// Absent values contribute nothing: a nil pointer, nil map or nil slice produces no
// leaf, so adding an unset optional key cannot change a hash.
package config

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// leaf is one scalar at a dotted configuration path.
type leaf struct {
	path  string
	value reflect.Value
}

// flatten returns every scalar reachable from v, prefixed with path. Map keys are
// visited in sorted order and slice elements by index, so the result is deterministic
// regardless of Go's map iteration order.
func flatten(path string, v reflect.Value) []leaf {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return flatten(path, v.Elem())
	case reflect.Struct:
		return flattenStruct(path, v)
	case reflect.Map:
		return flattenMap(path, v)
	case reflect.Slice, reflect.Array:
		var out []leaf
		for i := range v.Len() {
			out = append(out, flatten(join(path, strconv.Itoa(i)), v.Index(i))...)
		}
		return out
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return []leaf{{path: path, value: v}}
	default:
		// Functions, channels and the like are not configuration.
		return nil
	}
}

// flattenStruct walks exported fields using their mapstructure names, so paths match the
// keys an operator writes in YAML. Embedded squashed structs contribute their fields at
// the parent's path, matching how mapstructure decodes them.
func flattenStruct(path string, v reflect.Value) []leaf {
	var out []leaf
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported: unreachable and deliberately unhashed
			continue
		}
		name, squash := fieldKey(f)
		if name == "-" {
			continue
		}
		child := path
		if !squash {
			child = join(path, name)
		}
		out = append(out, flatten(child, v.Field(i))...)
	}
	return out
}

// flattenMap walks a map in sorted key order.
func flattenMap(path string, v reflect.Value) []leaf {
	if v.IsNil() {
		return nil
	}
	keys := make([]string, 0, v.Len())
	byKey := make(map[string]reflect.Value, v.Len())
	for _, k := range v.MapKeys() {
		s := mapKeyString(k)
		keys = append(keys, s)
		byKey[s] = v.MapIndex(k)
	}
	sort.Strings(keys)

	var out []leaf
	for _, k := range keys {
		out = append(out, flatten(join(path, k), byKey[k])...)
	}
	return out
}

// fieldKey returns the configuration key for a struct field and whether the field is an
// embedded struct whose keys live at the parent level.
func fieldKey(f reflect.StructField) (name string, squash bool) {
	tag := f.Tag.Get("mapstructure")
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	for _, opt := range parts[1:] {
		if opt == "squash" {
			squash = true
		}
	}
	return name, squash
}

// mapKeyString renders a map key as a path segment. Configuration maps decoded from YAML
// are usually keyed by string; anything else is rendered through its Go value.
func mapKeyString(k reflect.Value) string {
	if k.Kind() == reflect.String {
		return k.String()
	}
	return fmt.Sprint(k.Interface())
}

// join appends a path segment, tolerating an empty prefix.
func join(prefix, segment string) string {
	if prefix == "" {
		return segment
	}
	return prefix + "." + segment
}
