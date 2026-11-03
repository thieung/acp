package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Object is a JSON object that keeps its key order, so manifests and settings files keep the
// layout people wrote (agentkit before skills, hooks where they were).
type Object struct {
	keys []string
	vals map[string]any
}

func NewObject() *Object { return &Object{vals: map[string]any{}} }

// Obj builds an Object from alternating keys and values.
func Obj(kv ...any) *Object {
	o := NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Len and Keys accept a nil object (a missing nested object), like Get does.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

func (o *Object) Has(key string) bool { _, ok := o.Get(key); return ok }

// Set replaces a value in place or appends a new key.
func (o *Object) Set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *Object) Delete(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// SortKeys orders the keys alphabetically (Python's dict(sorted(...))).
func (o *Object) SortKeys() { sort.Strings(o.keys) }

func (o *Object) String(key string) string {
	v, _ := o.Get(key)
	s, _ := v.(string)
	return s
}

func (o *Object) StringOr(key, def string) string {
	if v, ok := o.Get(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func (o *Object) Object(key string) *Object {
	v, _ := o.Get(key)
	m, _ := v.(*Object)
	return m
}

func (o *Object) Array(key string) []any {
	v, _ := o.Get(key)
	a, _ := v.([]any)
	return a
}

// Strings returns the string items of an array value, skipping anything else.
func (o *Object) Strings(key string) []string {
	v, _ := o.Get(key)
	if s, ok := v.([]string); ok {
		return append([]string{}, s...)
	}
	return StringList(o.Array(key))
}

func (o *Object) Bool(key string) bool {
	v, _ := o.Get(key)
	b, _ := v.(bool)
	return b
}

func StringList(items []any) []string {
	out := []string{}
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func AnyList[T any](items []T) []any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

// Clone deep-copies objects and arrays.
func Clone(v any) any {
	switch t := v.(type) {
	case *Object:
		c := NewObject()
		for _, k := range t.keys {
			c.Set(k, Clone(t.vals[k]))
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, it := range t {
			c[i] = Clone(it)
		}
		return c
	}
	return v
}

func (o *Object) MarshalJSON() ([]byte, error) {
	if o == nil {
		return []byte("null"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := encodeCompact(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := encodeCompact(o.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func encodeCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// MarshalJSON encodes like Python's json.dumps(indent=2) plus a trailing newline.
func MarshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseJSON decodes into *Object, []any, string, json.Number, bool and nil.
func ParseJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, syntaxError(data, dec, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, syntaxError(data, dec, errors.New("unexpected data after the top-level value"))
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
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
		return nil, fmt.Errorf("unexpected %q", t)
	}
	return tok, nil
}

func syntaxError(data []byte, dec *json.Decoder, err error) error {
	off := dec.InputOffset()
	var se *json.SyntaxError
	if errors.As(err, &se) {
		off = se.Offset
	}
	if off > int64(len(data)) {
		off = int64(len(data))
	}
	line := 1 + bytes.Count(data[:off], []byte("\n"))
	return fmt.Errorf("line %d: %w", line, err)
}
