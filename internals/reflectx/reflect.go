// Package reflectx demonstrates safe, narrow uses of Go reflection.
// Reflection is useful at dynamic boundaries such as serialization and
// configuration, but ordinary application dispatch should usually remain
// statically typed.
package reflectx

import (
	"errors"
	"reflect"
)

var (
	ErrTarget      = errors.New("target must be a non-nil pointer to a struct")
	ErrField       = errors.New("field does not exist")
	ErrNotSettable = errors.New("field is not exported or not settable")
	ErrType        = errors.New("value is not assignable to field type")
)

// FieldNames returns the declared field names of T in declaration order.
// Non-struct types have no fields and return nil.
func FieldNames[T any]() []string {
	typ := reflect.TypeFor[T]()
	if typ.Kind() != reflect.Struct {
		return nil
	}

	names := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		names = append(names, field.Name)
	}
	return names
}

// Assert converts v to T using reflect.TypeAssert, returning false when its
// dynamic value is not assignable to T.
func Assert[T any](v reflect.Value) (T, bool) {
	return reflect.TypeAssert[T](v)
}

// SetField sets one exported field on a non-nil pointer to a struct.
// It rejects implicit conversions and reports invalid reflection operations
// as errors rather than allowing Value.Set to panic.
func SetField(target any, name string, value any) error {
	ptr := reflect.ValueOf(target)
	if !ptr.IsValid() || ptr.Kind() != reflect.Pointer || ptr.IsNil() {
		return ErrTarget
	}
	strct := ptr.Elem()
	if strct.Kind() != reflect.Struct {
		return ErrTarget
	}

	field := strct.FieldByName(name)
	if !field.IsValid() {
		return ErrField
	}
	if !field.CanSet() {
		return ErrNotSettable
	}

	if value == nil {
		switch field.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			field.SetZero()
			return nil
		default:
			return ErrType
		}
	}

	newValue := reflect.ValueOf(value)
	if !newValue.Type().AssignableTo(field.Type()) {
		return ErrType
	}
	field.Set(newValue)
	return nil
}
