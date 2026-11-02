// Package jsonx demonstrates encoding/json idioms:
//
//   - **Struct tags** name fields and control omission: `json:"id"` and
//     `json:"kind,omitempty"` (omitempty drops zero values, not just
//     empty strings — 0, false and nil go too).
//   - **Custom (Un)MarshalJSON** puts a value on the wire in a
//     human-friendly form (money as "12.34" instead of raw cents) and
//     parses it back losslessly.
//   - **json.Number** via Decoder.UseNumber keeps large integers exact;
//     the default decoding turns every number into float64 and silently
//     loses precision beyond 2^53.
//   - **json.RawMessage** defers decoding of a payload you want to
//     inspect structurally first, or decode later into a concrete type.
//   - **Embedded structs** are flattened: promoted fields appear at the
//     top level of the object.
//
// Domain: a billing event with an amount in cents.
package jsonx

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Money is an amount in integer cents, marshalled as a decimal string
// ("12.34") so no float ever touches it.
type Money struct {
	Cents int64
}

// MarshalJSON emits the quoted decimal form.
func (m Money) MarshalJSON() ([]byte, error) {
	sign := ""
	c := m.Cents
	if c < 0 {
		sign = "-"
		c = -c
	}
	return []byte(`"` + sign + strconv.FormatInt(c/100, 10) + "." +
		fmt.Sprintf("%02d", c%100) + `"`), nil
}

// UnmarshalJSON parses "12.34" (or "-0.05") back into cents.
func (m *Money) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return fmt.Errorf("jsonx: money must be a quoted decimal, got %s", s)
	}
	s = s[1 : len(s)-1]
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg, s = true, s[1:]
	}
	dot := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return fmt.Errorf("jsonx: money %q needs a decimal point", s)
	}
	whole, err := strconv.ParseInt(s[:dot], 10, 64)
	if err != nil {
		return err
	}
	frac := s[dot+1:]
	if len(frac) != 2 {
		return fmt.Errorf("jsonx: money %q needs exactly 2 decimal places", s)
	}
	hundredths, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return err
	}
	cents := whole*100 + hundredths
	if neg {
		cents = -cents
	}
	m.Cents = cents
	return nil
}

// Event is the wire object.
type Event struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind,omitempty"` // dropped when empty
	Amount Money    `json:"amount"`
	Refund bool     `json:"refund,omitempty"` // dropped when false
	Tags   []string `json:"tags,omitempty"`
}

// Header is embedded so its fields appear at the top level of Event's
// JSON — embedded structs are flattened, not nested.
type Header struct {
	Version int    `json:"version"`
	Source  string `json:"source"`
}

// SignedEvent composes Header by embedding.
type SignedEvent struct {
	Header
	Event
}

// Envelope defers decoding of Payload: it stays as raw bytes until the
// caller decides what it is.
type Envelope struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
