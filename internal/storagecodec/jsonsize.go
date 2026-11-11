package storagecodec

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSONSize bounds encoding/json's actual UTF8 output before it allocates a
// serialized copy. Raw codec values include HTML/U+2028 escaping overhead.
func JSONSize(value any, limit int) (int, error) {
	n := 0
	add := func(bytes int) error {
		if bytes > limit-n {
			return errors.New("JSON bytes limit")
		}
		n += bytes
		return nil
	}
	var walk func(reflect.Value) error
	quoted := func(s string) error {
		if err := add(2); err != nil {
			return err
		}
		for len(s) > 0 {
			r, consumed := utf8.DecodeRuneInString(s)
			s = s[consumed:]
			size := utf8.RuneLen(r)
			if r == utf8.RuneError && consumed == 1 {
				size = 6
			}
			switch r {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				size = 2
			case '<', '>', '&', '\u2028', '\u2029':
				size = 6
			default:
				if r < 32 {
					size = 6
				}
			}
			if err := add(size); err != nil {
				return err
			}
		}
		return nil
	}
	walk = func(v reflect.Value) error {
		if !v.IsValid() {
			return add(4)
		}
		for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return add(4)
			}
			v = v.Elem()
		}
		if v.Type() == reflect.TypeFor[json.RawMessage]() {
			if v.IsNil() {
				return add(4)
			}
			raw := v.Bytes()
			if !json.Valid(raw) {
				return errors.New("invalid raw JSON")
			}
			if err := add(len(raw)); err != nil {
				return err
			}
			for i, b := range raw {
				if b == '<' || b == '>' || b == '&' {
					if err := add(5); err != nil {
						return err
					}
				}
				if b == 0xe2 && i+2 < len(raw) && raw[i+1] == 0x80 && (raw[i+2] == 0xa8 || raw[i+2] == 0xa9) {
					if err := add(3); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if v.CanInterface() {
			if m, ok := v.Interface().(json.Marshaler); ok {
				raw, err := m.MarshalJSON()
				if err != nil {
					return err
				}
				return add(len(raw))
			}
		}
		switch v.Kind() {
		case reflect.String:
			return quoted(v.String())
		case reflect.Bool:
			if v.Bool() {
				return add(4)
			}
			return add(5)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return add(len(strconv.FormatInt(v.Int(), 10)))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return add(len(strconv.FormatUint(v.Uint(), 10)))
		case reflect.Float32, reflect.Float64:
			f := v.Float()
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return errors.New("invalid JSON number")
			}
			format := byte('f')
			if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
				format = 'e'
			}
			text := strconv.FormatFloat(f, format, -1, v.Type().Bits())
			text = strings.Replace(text, "e-0", "e-", 1)
			return add(len(text))
		case reflect.Slice, reflect.Array:
			if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 && !v.IsNil() {
				return add(2 + 4*((v.Len()+2)/3))
			}
			if v.Kind() == reflect.Slice && v.IsNil() {
				return add(4)
			}
			if err := add(2); err != nil {
				return err
			}
			for i := 0; i < v.Len(); i++ {
				if i > 0 {
					if err := add(1); err != nil {
						return err
					}
				}
				if err := walk(v.Index(i)); err != nil {
					return err
				}
			}
			return nil
		case reflect.Map:
			if v.IsNil() {
				return add(4)
			}
			if v.Type().Key().Kind() != reflect.String {
				return errors.New("unsupported JSON map key")
			}
			if err := add(2); err != nil {
				return err
			}
			iter := v.MapRange()
			count := 0
			for iter.Next() {
				if count > 0 {
					if err := add(1); err != nil {
						return err
					}
				}
				count++
				if err := quoted(iter.Key().String()); err != nil {
					return err
				}
				if err := add(1); err != nil {
					return err
				}
				if err := walk(iter.Value()); err != nil {
					return err
				}
			}
			return nil
		case reflect.Struct:
			if err := add(2); err != nil {
				return err
			}
			count := 0
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if !field.IsExported() {
					continue
				}
				tag := field.Tag.Get("json")
				name, opts, _ := strings.Cut(tag, ",")
				if name == "-" {
					continue
				}
				if name == "" {
					name = field.Name
				}
				f := v.Field(i)
				empty := f.IsZero()
				if f.Kind() == reflect.Slice || f.Kind() == reflect.Map || f.Kind() == reflect.String {
					empty = f.Len() == 0
				}
				if strings.Contains(opts, "omitempty") && empty {
					continue
				}
				if count > 0 {
					if err := add(1); err != nil {
						return err
					}
				}
				count++
				if err := quoted(name); err != nil {
					return err
				}
				if err := add(1); err != nil {
					return err
				}
				if err := walk(f); err != nil {
					return err
				}
			}
			return nil
		default:
			return errors.New("unsupported JSON type")
		}
	}
	if limit < 0 {
		return 0, errors.New("invalid JSON limit")
	}
	err := walk(reflect.ValueOf(value))
	return n, err
}
