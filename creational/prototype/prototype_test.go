package prototype

import "testing"

func sample() Document {
	return Document{
		Title: "original",
		Tags:  []string{"a", "b"},
		Attrs: Attrs{"k": "v"},
		Refs:  []*Document{{Title: "child"}},
	}
}

func TestCloneCopiesTitle(t *testing.T) {
	d := sample()
	c := d.Clone()
	if c.Title != d.Title {
		t.Fatalf("want %q, got %q", d.Title, c.Title)
	}
}

func TestCloneIsDeepForSlices(t *testing.T) {
	d := sample()
	c := d.Clone()
	c.Tags[0] = "MUTATED"
	if d.Tags[0] != "a" {
		t.Fatal("mutating the clone's Tags changed the original")
	}
}

func TestCloneIsDeepForMaps(t *testing.T) {
	d := sample()
	c := d.Clone()
	c.Attrs["k"] = "MUTATED"
	c.Attrs["new"] = "x"
	if d.Attrs["k"] != "v" {
		t.Fatal("mutating the clone's Attrs changed the original")
	}
	if _, ok := d.Attrs["new"]; ok {
		t.Fatal("adding a key on the clone leaked into the original")
	}
}

func TestCloneIsDeepForNestedRefs(t *testing.T) {
	d := sample()
	c := d.Clone()
	c.Refs[0].Title = "MUTATED"
	if d.Refs[0].Title != "child" {
		t.Fatal("mutating the clone's nested ref changed the original")
	}
	if c.Refs[0] == d.Refs[0] {
		t.Fatal("nested ref pointer was shared, not cloned")
	}
}

func TestCloneHandlesZeroValue(t *testing.T) {
	var d Document
	c := d.Clone()
	if c.Title != "" || c.Tags != nil || c.Attrs != nil || c.Refs != nil {
		t.Fatalf("zero clone not zero: %+v", c)
	}
}

func TestCloneHandlesNilMapAndNilRefs(t *testing.T) {
	d := Document{Title: "x"} // Attrs and Refs are nil
	c := d.Clone()
	if c.Attrs != nil {
		t.Error("nil map should clone to nil")
	}
	if c.Refs != nil {
		t.Error("nil refs should clone to nil")
	}
}

func TestClonePtrNilSafe(t *testing.T) {
	var d *Document
	if got := d.ClonePtr(); got.Title != "" {
		t.Fatalf("nil clone should be zero, got %+v", got)
	}
}

func TestShallowCopyWouldHaveAliased(t *testing.T) {
	// Documents why Clone exists at all: plain assignment shares the slices.
	d := sample()
	shallow := d
	shallow.Tags[0] = "SHALLOW-ALIAS"
	if d.Tags[0] != "SHALLOW-ALIAS" {
		t.Fatal("test premise broken: assignment should share the slice")
	}
	deep := sample()
	deepCopy := deep.Clone()
	deepCopy.Tags[0] = "DEEP"
	if deep.Tags[0] != "a" {
		t.Fatal("Clone failed to isolate")
	}
}

func TestRegistryMakeReturnsIndependentCopies(t *testing.T) {
	r := NewRegistry()
	r.Register("report", sample())

	a, ok := r.Make("report")
	if !ok {
		t.Fatal("prototype not found")
	}
	b, _ := r.Make("report")

	a.Title = "A"
	b.Title = "B"
	a.Tags[0] = "X"

	if b.Tags[0] != "a" {
		t.Fatal("two Mains shared state")
	}

	orig, _ := r.Make("report")
	if orig.Title != "original" || orig.Tags[0] != "a" {
		t.Fatal("registry template was mutated by a Make")
	}
}

func TestRegistryMakeUnknownName(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Make("nope"); ok {
		t.Fatal("expected miss")
	}
}

func TestRegistryNames(t *testing.T) {
	r := NewRegistry()
	r.Register("a", Document{})
	r.Register("b", Document{})
	if len(r.Names()) != 2 {
		t.Fatalf("want 2 names, got %v", r.Names())
	}
}
