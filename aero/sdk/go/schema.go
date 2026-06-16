package aero

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

var (
	// ErrSchemaValidation indicates params failed JSON schema/type validation.
	ErrSchemaValidation = errors.New("aero: schema validation failed")
)

// Validator is implemented by param types that require custom validation.
type Validator interface {
	Validate() error
}

// UnmarshalSchema decodes JSON-RPC params into T and runs optional Validator.
func UnmarshalSchema[T any](raw json.RawMessage) (T, error) {
	var zero T
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return zero, nil
	}

	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return zero, fmt.Errorf("%w: %v", ErrSchemaValidation, err)
	}
	if err := validateValue(v); err != nil {
		return zero, err
	}
	return v, nil
}

// ValidateSchema checks that raw JSON matches the shape of T without returning the value.
func ValidateSchema[T any](raw json.RawMessage) error {
	_, err := UnmarshalSchema[T](raw)
	return err
}

func validateValue(v any) error {
	if validator, ok := v.(Validator); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrSchemaValidation, err)
		}
	}
	return validateRequiredFields(reflect.ValueOf(v))
}

func validateRequiredFields(v reflect.Value) error {
	if !v.IsValid() {
		return nil
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" { // unexported
			continue
		}
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := field.Name
		if tag != "" {
			name = strings.Split(tag, ",")[0]
		}
		if name == "" || name == ",omitempty" {
			continue
		}
		if bytes.Contains([]byte(tag), []byte("omitempty")) {
			continue
		}
		fv := v.Field(i)
		if isZero(fv) {
			return fmt.Errorf("%w: missing required field %q", ErrSchemaValidation, name)
		}
	}
	return nil
}

func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.Len() == 0
	case reflect.Slice, reflect.Map:
		return v.IsNil()
	default:
		return v.IsZero()
	}
}
