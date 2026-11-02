// Package prototype demonstrates the Prototype pattern in Go.
//
// Go has no prototype registry and no Cloneable interface; the language
// already copies values with `=`. What Go *does* need is an explicit deep
// copy for types that contain reference fields (slices, maps, pointers),
// because `=` on such a type produces a shallow copy whose fields alias the
// original — a classic bug source.
//
// Convention adopted here: a value receiver makes the shallow copy visible,
// and a Clone method makes the deep copy explicit and auditable. Reflect-
// based deep-copy helpers are deliberately avoided: they are slow, silently
// wrong across unexported fields, and unreviewable.
package prototype

// Attrs is a map type given its own Clone so nested copying stays obvious.
type Attrs map[string]string

// Clone returns an independent copy of the map. A nil map clones to nil.
func (a Attrs) Clone() Attrs {
	if a == nil {
		return nil
	}
	out := make(Attrs, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

// Document is a value type with reference-typed fields. It must be cloned
// explicitly before mutation.
type Document struct {
	Title string
	Tags  []string
	Attrs Attrs
	Refs  []*Document // nested prototypes: cloned recursively
}

// Clone returns a fully independent copy. It is safe to call on a zero
// Document and safe to call on a nil *Document (returns the zero value).
//
// Every reference-typed field is rebuilt, including nested Refs, so mutating
// the clone can never be observed through the original.
func (d Document) Clone() Document {
	out := Document{Title: d.Title}

	if d.Tags != nil {
		out.Tags = make([]string, len(d.Tags))
		copy(out.Tags, d.Tags)
	}

	out.Attrs = d.Attrs.Clone()

	if d.Refs != nil {
		out.Refs = make([]*Document, len(d.Refs))
		for i, r := range d.Refs {
			if r == nil {
				continue
			}
			cp := r.Clone()
			out.Refs[i] = &cp
		}
	}
	return out
}

// Clone on a pointer receiver handles the nil case for callers holding
// *Document (common when Refs are involved).
func (d *Document) ClonePtr() Document {
	if d == nil {
		return Document{}
	}
	return d.Clone()
}

// Registry stores named prototypes that can be materialised on demand.
// This is the "clone instead of construct" half of the pattern: a caller
// asks for a template by name and gets an independent copy, so it can
// customise the result without disturbing the stored original.
type Registry struct {
	proto map[string]Document
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{proto: make(map[string]Document)}
}

// Register stores a prototype under a name (stored as a copy).
// Registering twice replaces the previous template.
func (r *Registry) Register(name string, d Document) {
	r.proto[name] = d.Clone()
}

// Make returns a fresh, independent copy of the named prototype.
// The second result reports whether the name exists.
func (r *Registry) Make(name string) (Document, bool) {
	d, ok := r.proto[name]
	if !ok {
		return Document{}, false
	}
	return d.Clone(), true
}

// Names lists the registered prototype names.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.proto))
	for k := range r.proto {
		out = append(out, k)
	}
	return out
}
