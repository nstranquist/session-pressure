package jsonutil

import (
	"bytes"
	"fmt"
	"reflect"
	"time"
)

// RobotResponse matches the envelope emitted by bash scripts/robot_output.py.
// It is the canonical JSON envelope for ndev --json output.
type RobotResponse struct {
	Success   bool        `json:"success"`
	Timestamp string      `json:"timestamp"`
	Error     *string     `json:"error"`
	ErrorCode *string     `json:"error_code"`
	Hint      *string     `json:"hint"`
	Data      interface{} `json:"data"`
}

// RobotOk builds a success envelope wrapping data.
func RobotOk(data interface{}, hint string) RobotResponse {
	r := RobotResponse{
		Success:   true,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Data:      data,
	}
	if hint != "" {
		r.Hint = &hint
	}
	return r
}

// RobotErr builds an error envelope.
func RobotErr(error string, code string, hint string) RobotResponse {
	r := RobotResponse{
		Success:   false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Error:     &error,
		ErrorCode: &code,
	}
	if hint != "" {
		r.Hint = &hint
	}
	return r
}

// ToMap converts the response to a map[string]any so that jsonutil.Marshal
// emits keys in lexicographic order.
func (r RobotResponse) ToMap() map[string]any {
	m := map[string]any{
		"success":   r.Success,
		"timestamp": r.Timestamp,
		"data":      r.Data,
	}
	if r.Error != nil {
		m["error"] = *r.Error
	} else {
		m["error"] = nil
	}
	if r.ErrorCode != nil {
		m["error_code"] = *r.ErrorCode
	} else {
		m["error_code"] = nil
	}
	if r.Hint != nil {
		m["hint"] = *r.Hint
	} else {
		m["hint"] = nil
	}
	return m
}

// MarshalJSON implements json.Marshaler so that encoding/json callers
// also get the canonical sorted-key representation.
func (r RobotResponse) MarshalJSON() ([]byte, error) {
	return Marshal(r.ToMap())
}

// structToMap converts a struct (or pointer to struct) to a map[string]any
// using json tag names. It returns (nil, false) for non-struct types.
func structToMap(v any) (map[string]any, bool) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, false
	}
	m := make(map[string]any, rv.NumField())
	rt := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		field := rt.Field(i)
		if field.PkgPath != "" {
			continue // unexported
		}
		name := field.Name
		if tag := field.Tag.Get("json"); tag != "" {
			parts := splitComma(tag)
			if len(parts) > 0 && parts[0] == "-" {
				continue
			}
			// An empty name (json:",omitempty") retains the Go field name,
			// matching encoding/json. Never emit the tag sentinel as a key.
			if len(parts) > 0 && parts[0] != "" {
				name = parts[0]
			}
		}
		// Skip omitempty fields that are zero-valued, matching encoding/json.
		if hasOmitempty(field.Tag.Get("json")) && isZeroValue(rv.Field(i)) {
			continue
		}
		m[name] = rv.Field(i).Interface()
	}
	return m, true
}

func hasOmitempty(tag string) bool {
	for _, part := range splitComma(tag) {
		if part == "omitempty" {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Complex64, reflect.Complex128:
		return v.Complex() == 0
	case reflect.String:
		return v.String() == ""
	case reflect.Array, reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Struct:
		return false // conservative; encoding/json checks each field recursively
	}
	return false
}

// EncodeStruct emits a struct as compact JSON with sorted keys.
// It is used by encode when it encounters a struct value.
func encodeStruct(buf *bytes.Buffer, v any) error {
	m, ok := structToMap(v)
	if !ok {
		return fmt.Errorf("jsonutil: encodeStruct called with non-struct %T", v)
	}
	return encodeMap(buf, m)
}
