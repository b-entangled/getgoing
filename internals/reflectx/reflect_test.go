package reflectx

import (
	"reflect"
	"testing"
)

type record struct {
	ID     int
	Name   string
	hidden bool
}

func TestFieldNamesUsesGenericTypeMetadata(t *testing.T) {
	got := FieldNames[record]()
	want := []string{"ID", "Name", "hidden"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FieldNames[record]() = %v, want %v", got, want)
	}
}

func TestFieldNamesOfNonStructIsNil(t *testing.T) {
	if got := FieldNames[int](); got != nil {
		t.Fatalf("FieldNames[int]() = %v, want nil", got)
	}
}

func TestReflectTypeAssertMatchesDynamicType(t *testing.T) {
	got, ok := Assert[int](reflect.ValueOf(42))
	if !ok || got != 42 {
		t.Fatalf("Assert[int](42) = (%d, %v), want (42, true)", got, ok)
	}
	if _, ok := Assert[string](reflect.ValueOf(42)); ok {
		t.Fatal("Assert[string](42) succeeded for an int")
	}
}

func TestSetFieldRequiresAssignableExportedField(t *testing.T) {
	var got record
	if err := SetField(&got, "ID", 7); err != nil {
		t.Fatalf("SetField(ID) error = %v", err)
	}
	if got.ID != 7 {
		t.Fatalf("ID = %d, want 7", got.ID)
	}
	if err := SetField(&got, "hidden", true); err != ErrNotSettable {
		t.Fatalf("SetField(hidden) error = %v, want %v", err, ErrNotSettable)
	}
	if err := SetField(&got, "ID", "seven"); err != ErrType {
		t.Fatalf("SetField(wrong type) error = %v, want %v", err, ErrType)
	}
	if err := SetField(got, "ID", 8); err != ErrTarget {
		t.Fatalf("SetField(non-pointer) error = %v, want %v", err, ErrTarget)
	}
}

func TestSetFieldCanZeroNilableField(t *testing.T) {
	type optional struct{ Label *string }
	label := "present"
	got := optional{Label: &label}
	if err := SetField(&got, "Label", nil); err != nil {
		t.Fatalf("SetField(nil) error = %v", err)
	}
	if got.Label != nil {
		t.Fatal("nil assignment did not clear the pointer field")
	}
}
