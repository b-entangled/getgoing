package jsonx

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestMoneyRoundTripsThroughJSON(t *testing.T) {
	in := Money{Cents: 1234} // 12.34
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"12.34"` {
		t.Fatalf("marshalled as %s, want \"12.34\"", b)
	}
	var out Money
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Cents != in.Cents {
		t.Fatalf("round trip: %d, want %d", out.Cents, in.Cents)
	}
}

func TestMoneyRejectsMalformedInput(t *testing.T) {
	var m Money
	for _, bad := range []string{`12.34`, `"12"`, `"12.3"`, `"x.34"`} {
		if err := json.Unmarshal([]byte(bad), &m); err == nil {
			t.Errorf("Unmarshal(%s) succeeded, want error", bad)
		}
	}
}

// omitempty drops zero values: empty string, false, empty slice — and 0.
func TestOmitEmptyDropsZeroValues(t *testing.T) {
	b, err := json.Marshal(Event{ID: "e1", Amount: Money{Cents: 5}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, absent := range []string{"kind", "refund", "tags"} {
		if strings.Contains(s, `"`+absent+`"`) {
			t.Errorf("%s should be omitted from %s", absent, s)
		}
	}
	if !strings.Contains(s, `"id":"e1"`) {
		t.Errorf("id missing from %s", s)
	}

	// A non-zero value comes back.
	b, _ = json.Marshal(Event{ID: "e1", Kind: "charge", Refund: true, Amount: Money{Cents: 5}})
	s = string(b)
	if !strings.Contains(s, `"kind":"charge"`) || !strings.Contains(s, `"refund":true`) {
		t.Errorf("non-zero fields missing from %s", s)
	}
}

// Default decoding: every number becomes float64 — and precision beyond
// 2^53 is silently lost, which is why money should never be a bare number.
func TestDefaultDecodingLosesIntegerPrecision(t *testing.T) {
	const big = `{"n":12345678901234567890}`
	var v struct {
		N float64 `json:"n"`
	}
	if err := json.Unmarshal([]byte(big), &v); err != nil {
		t.Fatal(err)
	}
	// Rendering the float back gives a *different* number: the low digits
	// were rounded away when it became a float64.
	got := strconv.FormatFloat(v.N, 'f', -1, 64)
	if got == "12345678901234567890" {
		t.Fatal("expected float64 rounding to be observable")
	}
	if !strings.HasPrefix(got, "1234567890123456") {
		t.Fatalf("unexpected rounded value %s", got)
	}
}

// UseNumber keeps numbers as json.Number (a string), so integers survive.
func TestUseNumberPreservesIntegerPrecision(t *testing.T) {
	const big = `{"n":12345678901234567890}`
	dec := json.NewDecoder(bytes.NewReader([]byte(big)))
	dec.UseNumber()
	var v struct {
		N json.Number `json:"n"`
	}
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if _, err := v.N.Int64(); err == nil {
		t.Fatal("value exceeds int64; expected an error")
	}
	if got := v.N.String(); got != "12345678901234567890" {
		t.Fatalf("precision lost: %s", got)
	}
}

func TestRawMessageDefersDecoding(t *testing.T) {
	raw := []byte(`{"kind":"charge","payload":{"cents":999}}`)
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	// The envelope decoded without touching the payload.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["cents"]; !ok {
		t.Fatalf("payload keys: %v", probe)
	}
	// And it can still be decoded into a real type later.
	var payload struct {
		Cents int64 `json:"cents"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Cents != 999 {
		t.Fatalf("cents = %d", payload.Cents)
	}
}

// Embedded struct fields are promoted: they appear at the top level.
func TestEmbeddedStructFieldsAreFlattened(t *testing.T) {
	in := SignedEvent{
		Header: Header{Version: 2, Source: "billing"},
		Event:  Event{ID: "e9", Amount: Money{Cents: 700}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version":2`, `"source":"billing"`, `"id":"e9"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(string(b), `"Header"`) || strings.Contains(string(b), `"Event"`) {
		t.Errorf("embedded struct must not nest: %s", b)
	}
}
