package storagecodec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/mvanhorn/agentcookie/internal/protocol"
)

// Validate bounds and checks the tagged graph before CDP allocation. The same
// graph is validated by codec.js before construction in the browser world.
func Validate(raw json.RawMessage) error {
	if len(raw) > protocol.MaxRecordBytes {
		return errors.New("codec bytes limit")
	}
	v := validator{refs: map[uint64]string{}}
	return v.value(raw, 0)
}
func ValidateString(raw json.RawMessage) error {
	if err := Validate(raw); err != nil {
		return err
	}
	var o map[string]json.RawMessage
	if err := json.Unmarshal(raw, &o); err != nil {
		return err
	}
	var tag string
	_ = json.Unmarshal(o["t"], &tag)
	if tag != "str" && tag != "str16" {
		return errors.New("expected codec string")
	}
	return nil
}

type validator struct {
	nodes     int
	refs      map[uint64]string
	allocated uint64
}

func number(raw json.RawMessage) (float64, error) {
	var n float64
	err := json.Unmarshal(raw, &n)
	if err != nil || len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, errors.New("invalid number")
	}
	return n, nil
}
func integer(raw json.RawMessage) (uint64, error) {
	n, err := number(raw)
	if err != nil || n < 0 || math.Trunc(n) != n || n > 9007199254740991 {
		return 0, errors.New("invalid integer")
	}
	return uint64(n), nil
}
func textValue(raw json.RawMessage) (string, error) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || !utf8.Valid(raw) {
		return "", errors.New("invalid string")
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}
func stringWellFormed(raw json.RawMessage) bool {
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			lo, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || lo < 0xdc00 || lo > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
func (v *validator) value(raw json.RawMessage, depth int) error {
	v.nodes++
	if v.nodes > protocol.MaxValueNodes {
		return errors.New("codec nodes limit")
	}
	if depth > protocol.MaxValueDepth {
		return errors.New("codec depth limit")
	}
	var o map[string]json.RawMessage
	if err := json.Unmarshal(raw, &o); err != nil || o == nil {
		return errors.New("invalid codec object")
	}
	tag, err := textValue(o["t"])
	if err != nil {
		return err
	}
	object := false
	switch tag {
	case "date", "regexp", "arr", "obj", "map", "set", "buf", "view", "boxed":
		object = true
	}
	if object {
		id, err := integer(o["id"])
		if err != nil || id != uint64(len(v.refs)+1) {
			return errors.New("invalid object id")
		}
		v.refs[id] = tag
	}
	child := func(r json.RawMessage) error { return v.value(r, depth+1) }
	list := func(r json.RawMessage) ([]json.RawMessage, error) {
		var a []json.RawMessage
		if len(r) == 0 || r[0] != '[' {
			return nil, errors.New("expected list")
		}
		err := json.Unmarshal(r, &a)
		return a, err
	}
	switch tag {
	case "null", "undef":
		return nil
	case "bool":
		if !bytes.Equal(o["v"], []byte("true")) && !bytes.Equal(o["v"], []byte("false")) {
			return errors.New("invalid bool")
		}
		return nil
	case "num":
		n, err := number(o["v"])
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) || math.Signbit(n) && n == 0 {
			return errors.New("invalid num")
		}
		return nil
	case "numx":
		s, err := textValue(o["v"])
		if err != nil {
			return err
		}
		switch s {
		case "NaN", "Infinity", "-Infinity", "-0":
			return nil
		}
		return errors.New("invalid numx")
	case "bigint":
		s, err := textValue(o["v"])
		if err != nil || s == "" {
			return errors.New("invalid bigint")
		}
		if s[0] == '-' {
			s = s[1:]
		}
		if s == "" || len(s) > 1 && s[0] == '0' {
			return errors.New("invalid bigint")
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return errors.New("invalid bigint")
			}
		}
		return nil
	case "str":
		if _, err := textValue(o["v"]); err != nil {
			return err
		}
		if !stringWellFormed(o["v"]) {
			return errors.New("unpaired UTF16 requires str16")
		}
		return nil
	case "str16":
		a, err := list(o["v"])
		if err != nil {
			return err
		}
		for _, r := range a {
			n, err := integer(r)
			if err != nil || n > 65535 {
				return errors.New("invalid UTF16 unit")
			}
		}
		return nil
	case "ref":
		id, err := integer(o["id"])
		if err != nil || v.refs[id] == "" {
			return errors.New("invalid reference")
		}
		return nil
	case "date":
		if bytes.Equal(o["v"], []byte(`"NaN"`)) {
			return nil
		}
		n, err := number(o["v"])
		if err != nil || math.Abs(n) > 8640000000000000 || math.Trunc(n) != n {
			return errors.New("invalid date")
		}
		return nil
	case "regexp":
		if err := ValidateString(o["src"]); err != nil {
			return err
		}
		flags, err := textValue(o["flags"])
		if err != nil {
			return err
		}
		seen := map[rune]bool{}
		for _, r := range flags {
			if !bytes.ContainsRune([]byte("dgimsuvy"), r) || seen[r] {
				return errors.New("invalid regexp flags")
			}
			seen[r] = true
		}
		if seen['u'] && seen['v'] {
			return errors.New("invalid regexp flags")
		}
		return nil
	case "buf":
		s, err := textValue(o["b64"])
		if err != nil {
			return err
		}
		if base64.StdEncoding.DecodedLen(len(s)) > protocol.MaxRecordBytes {
			return errors.New("buffer bytes limit")
		}
		_, err = base64.StdEncoding.Strict().DecodeString(s)
		return err
	case "view":
		kind, err := textValue(o["kind"])
		if err != nil {
			return err
		}
		switch kind {
		case "Int8Array", "Uint8Array", "Uint8ClampedArray", "Int16Array", "Uint16Array", "Int32Array", "Uint32Array", "Float16Array", "Float32Array", "Float64Array", "BigInt64Array", "BigUint64Array", "DataView":
		default:
			return errors.New("invalid view kind")
		}
		if _, err := integer(o["off"]); err != nil {
			return err
		}
		if _, err := integer(o["len"]); err != nil {
			return err
		}
		return child(o["buf"])
	case "boxed":
		kind, err := textValue(o["kind"])
		if err != nil {
			return err
		}
		switch kind {
		case "Number", "String", "Boolean", "BigInt":
		default:
			return errors.New("invalid boxed kind")
		}
		return child(o["v"])
	case "arr":
		length, err := integer(o["len"])
		if err != nil || length > 4294967295 {
			return errors.New("invalid array length")
		}
		v.allocated += length
		if v.allocated > protocol.MaxRecordBytes {
			return errors.New("array bytes limit")
		}
		items, err := list(o["items"])
		if err != nil {
			return err
		}
		var last uint64
		for n, r := range items {
			pair, err := list(r)
			if err != nil || len(pair) != 2 {
				return errors.New("invalid array item")
			}
			i, err := integer(pair[0])
			if err != nil || i >= length || n > 0 && i <= last {
				return errors.New("invalid array index")
			}
			last = i
			if err := child(pair[1]); err != nil {
				return err
			}
		}
		return nil
	case "obj", "map":
		field := "props"
		if tag == "map" {
			field = "entries"
		}
		items, err := list(o[field])
		if err != nil {
			return err
		}
		for _, r := range items {
			pair, err := list(r)
			if err != nil || len(pair) != 2 {
				return errors.New("invalid pair")
			}
			if tag == "obj" {
				if err := ValidateString(pair[0]); err != nil {
					return err
				}
			} else if err := child(pair[0]); err != nil {
				return err
			}
			if err := child(pair[1]); err != nil {
				return err
			}
		}
		return nil
	case "set":
		items, err := list(o["entries"])
		if err != nil {
			return err
		}
		for _, r := range items {
			if err := child(r); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported codec tag %q", tag)
	}
}

// StringUnits counts storage's native UTF16 units, not UTF8 wire bytes.
func StringUnits(raw json.RawMessage) (int, error) {
	if err := ValidateString(raw); err != nil {
		return 0, err
	}
	var o struct {
		T string          `json:"t"`
		V json.RawMessage `json:"v"`
	}
	_ = json.Unmarshal(raw, &o)
	if o.T == "str16" {
		var units []uint16
		if err := json.Unmarshal(o.V, &units); err != nil {
			return 0, err
		}
		return len(units), nil
	}
	var s string
	if err := json.Unmarshal(o.V, &s); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n, nil
}

func ValidateKey(raw json.RawMessage) error {
	if err := Validate(raw); err != nil {
		return err
	}
	closed := map[uint64]bool{}
	var walk func(json.RawMessage) error
	walk = func(raw json.RawMessage) error {
		var o map[string]json.RawMessage
		_ = json.Unmarshal(raw, &o)
		tag, _ := textValue(o["t"])
		id, _ := integer(o["id"])
		switch tag {
		case "num", "str", "str16", "buf":
		case "numx":
			s, _ := textValue(o["v"])
			if s != "Infinity" && s != "-Infinity" && s != "-0" {
				return errors.New("invalid IDB key number")
			}
		case "date":
			if bytes.Equal(o["v"], []byte(`"NaN"`)) {
				return errors.New("invalid IDB key date")
			}
		case "view":
			if err := walk(o["buf"]); err != nil {
				return err
			}
		case "arr":
			length, _ := integer(o["len"])
			var items [][]json.RawMessage
			_ = json.Unmarshal(o["items"], &items)
			if uint64(len(items)) != length {
				return errors.New("IDB key array has holes")
			}
			for _, p := range items {
				if err := walk(p[1]); err != nil {
					return err
				}
			}
		case "ref":
			if !closed[id] {
				return errors.New("IDB key has cyclic reference")
			}
		default:
			return errors.New("invalid IDB key type")
		}
		if id > 0 {
			closed[id] = true
		}
		return nil
	}
	return walk(raw)
}
