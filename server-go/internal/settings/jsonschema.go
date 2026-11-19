// Mini JSON Schema (draft 2020-12 subset) validator.
//
// The Deno server validates request bodies with @cfworker/json-schema@4.1.1
// and formats the first error as the 400 message (server/lib/api/validate.ts
// schemaErrorOf). To keep client-facing error bodies byte-identical after the
// rewrite, this engine replicates @cfworker's validation order, short-circuit
// behavior and error strings for the subset the internal schemas use:
// type, enum, required, properties, items, additionalProperties:false,
// minimum/maximum and the "uuid" / "date-time" formats.
package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// validationError is one @cfworker error entry; only the fields used for the
// API error message are kept.
type validationError struct {
	instanceLocation string
	message          string
}

// schemaProp is a named property. Order matters: @cfworker iterates schema
// properties in declaration order and short-circuits on the first failure.
type schemaProp struct {
	name   string
	schema *schema
}

// schema is the JSON Schema subset used by the internal schemas.
type schema struct {
	types    []string // e.g. "object", "integer", ["string", "null"]
	enum     []string
	format   string // "uuid", "date-time" or ""
	minimum  *float64
	maximum  *float64
	required []string
	props    []schemaProp
	items    *schema
	closed   bool
}

func floatPtr(f float64) *float64 { return &f }

// ---------------------------------------------------------------------------
// Ordered JSON decoding. encoding/json into map[string]any loses key order,
// which the additionalProperties error selection needs.

type orderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *orderedObject {
	return &orderedObject{values: make(map[string]any)}
}

func (o *orderedObject) lookup(key string) (any, bool) {
	v, ok := o.values[key]
	return v, ok
}

// decodeJSONValue decodes JSON preserving object key order. Like JS
// JSON.parse, duplicate keys keep their first position with the last value.
// Numbers are float64, object members are *orderedObject, arrays are []any.
func decodeJSONValue(data []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	v, err := decodeJSONToken(dec)
	if err != nil {
		return nil, err
	}
	// Reject trailing garbage (JSON.parse fails on it).
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid JSON body")
	}
	return v, nil
}

func decodeJSONToken(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := newOrderedObject()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("invalid object key")
				}
				v, err := decodeJSONToken(dec)
				if err != nil {
					return nil, err
				}
				if _, exists := obj.values[key]; !exists {
					obj.keys = append(obj.keys, key)
				}
				obj.values[key] = v
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeJSONToken(dec)
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
		return nil, fmt.Errorf("unexpected delimiter %q", string(rune(t)))
	default:
		return tok, nil
	}
}

// ---------------------------------------------------------------------------
// Validation engine (mirrors @cfworker/json-schema's validate.js).

// validateSchemaValue appends validation errors for inst to errs, in
// @cfworker's order. instanceTypeOf(inst) is asserted via a type switch.
func validateSchemaValue(inst any, sch *schema, loc string, errs *[]validationError) {
	inst = coerceValue(inst)
	typ := instanceTypeOf(inst)

	if len(sch.types) > 0 && !matchesType(typ, inst, sch.types) {
		*errs = append(*errs, validationError{loc, typeErrorMessage(typ, sch.types)})
		return
	}
	if len(sch.enum) > 0 && !enumContains(sch.enum, inst) {
		*errs = append(*errs, validationError{
			loc,
			`Instance does not match any of ` + compactJSON(sch.enum) + `.`,
		})
		return
	}

	switch v := inst.(type) {
	case *orderedObject:
		for _, key := range sch.required {
			if _, ok := v.lookup(key); !ok {
				*errs = append(*errs, validationError{
					loc,
					fmt.Sprintf("Instance does not have required property %q.", key),
				})
			}
		}
		stop := false
		for _, p := range sch.props {
			pv, present := v.lookup(p.name)
			if !present {
				continue
			}
			var sub []validationError
			validateSchemaValue(pv, p.schema, loc+"/"+encodePointer(p.name), &sub)
			if len(sub) > 0 {
				*errs = append(*errs, validationError{
					loc,
					fmt.Sprintf("Property %q does not match schema.", p.name),
				})
				*errs = append(*errs, sub...)
				stop = true
				break
			}
		}
		if !stop && sch.closed {
			for _, key := range v.keys {
				if hasSchemaProp(sch.props, key) {
					continue
				}
				*errs = append(*errs, validationError{
					loc,
					fmt.Sprintf("Property %q does not match additional properties schema.", key),
				})
				*errs = append(*errs, validationError{
					loc + "/" + encodePointer(key),
					"False boolean schema.",
				})
				break
			}
		}
	case []any:
		if sch.items == nil {
			return
		}
		for i, el := range v {
			var sub []validationError
			validateSchemaValue(el, sch.items, loc+"/"+strconv.Itoa(i), &sub)
			if len(sub) > 0 {
				*errs = append(*errs, validationError{loc, "Items did not match schema."})
				*errs = append(*errs, sub...)
				break
			}
		}
	case float64:
		if sch.minimum != nil && v < *sch.minimum {
			*errs = append(*errs, validationError{
				loc,
				fmt.Sprintf("%s is less than %s.", jsNumber(v), jsNumber(*sch.minimum)),
			})
		}
		if sch.maximum != nil && v > *sch.maximum {
			*errs = append(*errs, validationError{
				loc,
				fmt.Sprintf("%s is greater than %s.", jsNumber(v), jsNumber(*sch.maximum)),
			})
		}
	case string:
		switch sch.format {
		case "uuid":
			if !validUUIDFormat(v) {
				*errs = append(*errs, validationError{
					loc, `String does not match format "uuid".`,
				})
			}
		case "date-time":
			if !validDateTimeFormat(v) {
				*errs = append(*errs, validationError{
					loc, `String does not match format "date-time".`,
				})
			}
		}
	}
}

// validateAgainst returns ("", true) when value matches sch, or the
// schemaErrorOf-formatted first error otherwise.
func validateAgainst(sch *schema, value any) (string, bool) {
	var errs []validationError
	validateSchemaValue(value, sch, "#", &errs)
	if len(errs) == 0 {
		return "", true
	}
	return schemaErrorMessage(errs), false
}

// schemaErrorMessage formats the first error the way Deno's schemaErrorOf
// does: "<instanceLocation>: <error>" for nested errors, bare otherwise.
func schemaErrorMessage(errs []validationError) string {
	if len(errs) == 0 {
		return "invalid request body"
	}
	first := errs[0]
	if first.instanceLocation != "" && first.instanceLocation != "#" {
		return first.instanceLocation + ": " + first.message
	}
	return first.message
}

// relaxClosed returns a shallow copy of sch with additionalProperties relaxed
// (stored-value reads tolerate unknown keys, like Deno's matchesStoredSchema).
func relaxClosed(sch *schema) *schema {
	cp := *sch
	cp.closed = false
	return &cp
}

// coerceValue normalizes decoded values for validation: map[string]any (from
// a plain encoding/json decode) becomes an orderedObject.
func coerceValue(inst any) any {
	switch v := inst.(type) {
	case map[string]any:
		obj := newOrderedObject()
		for k, val := range v {
			obj.keys = append(obj.keys, k)
			obj.values[k] = coerceJSONNumber(val)
		}
		return obj
	case json.Number:
		// UseNumber-decoded leaves (store.decodeObject) must be visible
		// to instanceTypeOf or guards silently report "unknown" types.
		return coerceJSONNumber(v)
	default:
		return inst
	}
}

// coerceJSONNumber settles a json.Number into the float64 the validator
// type-switches on; validation paths never re-marshal, so the loss of the
// literal form is harmless.
func coerceJSONNumber(inst any) any {
	if n, ok := inst.(json.Number); ok {
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return inst
}

// coerceStoredValue accepts raw JSON bytes or an already-decoded value and
// normalizes map[string]any into the ordered form.
func coerceStoredValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil:
		return nil, true
	case bool, string, float64:
		return v, true
	case []byte:
		decoded, err := decodeJSONValue(v)
		if err != nil {
			return nil, false
		}
		return decoded, true
	case json.RawMessage:
		// json.RawMessage is a distinct dynamic type: it does not match
		// the []byte case above, yet callers hand it over as-is.
		decoded, err := decodeJSONValue(v)
		if err != nil {
			return nil, false
		}
		return decoded, true
	case []any, *orderedObject:
		return v, true
	case map[string]any:
		return coerceValue(v), true
	default:
		return nil, false
	}
}

func hasSchemaProp(props []schemaProp, key string) bool {
	for _, p := range props {
		if p.name == key {
			return true
		}
	}
	return false
}

func enumContains(enum []string, inst any) bool {
	s, ok := inst.(string)
	if !ok {
		return false
	}
	for _, e := range enum {
		if e == s {
			return true
		}
	}
	return false
}

func compactJSON(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		// Encoder without HTML escaping, matching JSON.stringify so an
		// enum value containing < & > echoes verbatim.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return fmt.Sprintf("%v", values)
		}
		parts[i] = strings.TrimRight(buf.String(), "\n")
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func instanceTypeOf(inst any) string {
	switch inst.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case *orderedObject:
		return "object"
	default:
		return "unknown"
	}
}

func matchesType(typ string, inst any, types []string) bool {
	for _, t := range types {
		if t == typ {
			return true
		}
		if t == "integer" && typ == "number" {
			if f, ok := inst.(float64); ok && f == trunc(f) {
				return true
			}
		}
	}
	return false
}

func typeErrorMessage(typ string, types []string) string {
	quoted := make([]string, len(types))
	for i, t := range types {
		quoted[i] = `"` + t + `"`
	}
	return fmt.Sprintf("Instance type %q is invalid. Expected %s.", typ, strings.Join(quoted, ", "))
}

func trunc(f float64) float64 {
	if f < 0 {
		return -trunc(-f)
	}
	return float64(int64(f))
}

// jsNumber renders a number the way JS template literals do (used in the
// minimum/maximum error messages).
func jsNumber(f float64) string {
	if f == trunc(f) && f < 1e15 && f > -1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func encodePointer(key string) string {
	// ~ then / escaping; the JS version also runs encodeURI, a no-op for the
	// ASCII field names these schemas use.
	s := strings.ReplaceAll(key, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}
