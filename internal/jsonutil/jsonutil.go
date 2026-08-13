// Package jsonutil provides deterministic JSON encoding that matches the
// byte-level output of bash `jq -c`.
//
// Required because the Go cutover's parity contract diffs Go output against
// bash output post-redaction. Any ordering or whitespace drift between the
// two encoders would fail the parity test even with identical semantic data.
//
// Contract:
//   - Keys in objects are emitted in lexicographic order.
//   - No trailing newline. No pretty-printing. No indentation.
//   - HTML-safe escaping is DISABLED (jq does not escape <, >, &).
//   - Floats use strconv.FormatFloat with -1 precision (shortest roundtrip).
package jsonutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// Marshal emits v as a compact, key-sorted JSON document.
//
// Accepts map[string]any, []any, string, bool, float64, int, int64, nil —
// plus any type json.Marshal accepts as a leaf (passed through unchanged).
// Nested structures are recursively canonicalized.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encode(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MustMarshal panics on error. Use in tests and in code paths where the input
// shape is statically known to be encodable.
func MustMarshal(v any) []byte {
	b, err := Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// CanonicalObject parses raw JSON, verifies the top-level value is an object,
// and emits it through Marshal.
func CanonicalObject(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("jsonutil: decode object: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("jsonutil: decode object: trailing JSON value")
		}
		return nil, fmt.Errorf("jsonutil: decode object: trailing data: %w", err)
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("jsonutil: top-level JSON value must be object")
	}
	out, err := Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jsonutil: encode object: %w", err)
	}
	return out, nil
}

func encode(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		return encodeString(buf, val)
	case int:
		buf.WriteString(strconv.FormatInt(int64(val), 10))
	case int64:
		buf.WriteString(strconv.FormatInt(val, 10))
	case json.Number:
		// Round-tripped from a Decoder with UseNumber(). Preserve the raw
		// source text so `2.0` stays `2.0` (jq's behavior), not collapsed to
		// `2`. This matters for byte-parity with bash jq output.
		buf.WriteString(val.String())
	case float64:
		return encodeFloat(buf, val)
	case map[string]any:
		return encodeMap(buf, val)
	case []any:
		return encodeSlice(buf, val)
	default:
		// Check for custom json.Marshaler first so RobotResponse and
		// other types with explicit MarshalJSON are respected.
		if jm, ok := val.(json.Marshaler); ok {
			b, err := jm.MarshalJSON()
			if err != nil {
				return fmt.Errorf("jsonutil: %w", err)
			}
			buf.Write(b)
			return nil
		}
		// For structs without a custom marshaler, convert to map[string]any
		// so keys are sorted lexicographically, matching jq -c behaviour.
		if m, ok := structToMap(val); ok {
			return encodeMap(buf, m)
		}
		// Fall through to encoding/json for everything else.
		b, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("jsonutil: %w", err)
		}
		buf.Write(b)
	}
	return nil
}

func encodeMap(buf *bytes.Buffer, m map[string]any) error {
	buf.WriteByte('{')
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := encodeString(buf, k); err != nil {
			return err
		}
		buf.WriteByte(':')
		if err := encode(buf, m[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

func encodeSlice(buf *bytes.Buffer, s []any) error {
	buf.WriteByte('[')
	for i, v := range s {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := encode(buf, v); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

func encodeString(buf *bytes.Buffer, s string) error {
	// We reuse encoding/json for escaping to stay consistent with Go's own
	// JSON encoder, then strip the surrounding quotes so we control them.
	// SetEscapeHTML(false) matches jq's behavior: <, >, & stay literal.
	b := bytes.NewBuffer(nil)
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("jsonutil: %w", err)
	}
	// json.Encoder appends a trailing newline after each value. Drop it.
	out := b.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	buf.Write(out)
	return nil
}

func encodeFloat(buf *bytes.Buffer, f float64) error {
	// Match jq's behavior: integer-valued floats emit without a decimal.
	if f == float64(int64(f)) {
		buf.WriteString(strconv.FormatInt(int64(f), 10))
		return nil
	}
	buf.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	return nil
}
