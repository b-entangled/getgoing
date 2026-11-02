package strategy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func pkg() Package {
	return Package{WeightKg: 2.5, DistanceKm: 120, ValueCents: 20000, DestZone: "zone-a"}
}

func TestFlatIgnoresEverythingElse(t *testing.T) {
	q, err := Flat(500, "flat")(context.Background(), pkg())
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 500 || q.Strategy != "flat" {
		t.Fatalf("got %+v", q)
	}
}

func TestFlatVariantsAreConfiguredClosures(t *testing.T) {
	ctx := context.Background()
	local := Flat(300, "local")
	express := Flat(900, "express")

	a, _ := local(ctx, pkg())
	b, _ := express(ctx, pkg())
	if a.Total == b.Total {
		t.Fatal("configured strategies should differ")
	}
	if a.Strategy != "local" || b.Strategy != "express" {
		t.Fatalf("got %q %q", a.Strategy, b.Strategy)
	}
}

func TestWeightBasedPicksCorrectTier(t *testing.T) {
	tiers := []Tier{
		{MaxKg: 1, Base: 150, PerKg: 50},
		{MaxKg: 5, Base: 300, PerKg: 40},
		{MaxKg: 20, Base: 800, PerKg: 20},
	}
	r := WeightBased("weight", tiers)

	light, err := r(context.Background(), Package{WeightKg: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	// base 150 + ceil(0.5*50)=25 → 175
	if light.Total != 175 {
		t.Fatalf("light quote: %s", light.Total)
	}

	heavy, _ := r(context.Background(), Package{WeightKg: 10})
	// base 800 + ceil(10*20)=200 → 1000
	if heavy.Total != 1000 {
		t.Fatalf("heavy quote: %s", heavy.Total)
	}
}

func TestWeightBasedRejectsOversize(t *testing.T) {
	r := WeightBased("weight", []Tier{{MaxKg: 1, Base: 100, PerKg: 10}})
	if _, err := r(context.Background(), Package{WeightKg: 5}); err == nil {
		t.Fatal("expected an error for a package exceeding all tiers")
	}
}

func TestPerKmAppliesFreeRadius(t *testing.T) {
	r := PerKm("distance", 200, 10, 50)
	// 120km − 50 free = 70 × 10 = 700, + 200 base = 900
	q, err := r(context.Background(), pkg())
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 900 {
		t.Fatalf("got %s", q.Total)
	}
}

func TestPerKmInsideFreeRadius(t *testing.T) {
	r := PerKm("distance", 200, 10, 500)
	q, err := r(context.Background(), Package{WeightKg: 1, DistanceKm: 100})
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 200 {
		t.Fatalf("want base only, got %s", q.Total)
	}
}

func TestInsuredValueWithMinimum(t *testing.T) {
	r := InsuredValue("insured", 0.01, 500)

	// 1% of 20000 = 200 → below minimum → 500
	q, _ := r(context.Background(), pkg())
	if q.Total != 500 {
		t.Fatalf("minimum not applied: %s", q.Total)
	}

	// 1% of 1_000_000 = 10000 → above minimum
	big, _ := r(context.Background(), Package{WeightKg: 1, ValueCents: 1_000_000})
	if big.Total != 10000 {
		t.Fatalf("got %s", big.Total)
	}
}

func TestValidationIsSharedAcrossStrategies(t *testing.T) {
	ctx := context.Background()
	bad := Package{WeightKg: -1}
	strategies := map[string]Rate{
		"flat":     Flat(100, "flat"),
		"weight":   WeightBased("w", []Tier{{MaxKg: 10, Base: 1, PerKg: 1}}),
		"distance": PerKm("d", 1, 1, 1),
		"insured":  InsuredValue("i", 0.1, 1),
	}
	for name, r := range strategies {
		if _, err := r(ctx, bad); err == nil {
			t.Errorf("%s accepted an invalid package", name)
		}
	}
}

func TestCancellationIsSharedAcrossStrategies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	strategies := map[string]Rate{
		"flat":     Flat(100, "flat"),
		"weight":   WeightBased("w", []Tier{{MaxKg: 10, Base: 1, PerKg: 1}}),
		"distance": PerKm("d", 1, 1, 1),
		"insured":  InsuredValue("i", 0.1, 1),
	}
	for name, r := range strategies {
		if _, err := r(ctx, pkg()); !errors.Is(err, context.Canceled) {
			t.Errorf("%s returned %v", name, err)
		}
	}
}

// A decorated strategy needs no wrapper type — Rate is already a func.
func TestSurchargeDecorator(t *testing.T) {
	base := Flat(1000, "flat")
	holiday := WithSurcharge(base, 0.10, "holiday")

	q, err := holiday(context.Background(), pkg())
	if err != nil {
		t.Fatal(err)
	}
	if q.Total != 1100 {
		t.Fatalf("got %s", q.Total)
	}
	if !strings.Contains(strings.Join(q.Breaks, "; "), "holiday +1.00") {
		t.Fatalf("breaks=%v", q.Breaks)
	}
}

func TestDecoratorForwardsErrors(t *testing.T) {
	failing := func(context.Context, Package) (RateQuote, error) {
		return RateQuote{}, errors.New("boom")
	}
	if _, err := WithSurcharge(failing, 0.1, "x")(context.Background(), pkg()); err == nil {
		t.Fatal("decorator swallowed the error")
	}
}

// Runtime selection — the point of the pattern.
func TestCheapestOfSelectsMinimum(t *testing.T) {
	r := CheapestOf(
		Flat(900, "flat"),
		WeightBased("weight", []Tier{{MaxKg: 5, Base: 100, PerKg: 10}}),
		PerKm("distance", 500, 2, 100),
	)
	q, err := r(context.Background(), pkg())
	if err != nil {
		t.Fatal(err)
	}
	// weight: 100 + ceil(2.5*10)=25 → 125 (cheapest)
	if q.Total != 125 || q.Strategy != "weight" {
		t.Fatalf("got %+v", q)
	}
	if !strings.Contains(strings.Join(q.Breaks, " "), "cheapest") {
		t.Fatalf("breaks=%v", q.Breaks)
	}
}

func TestCheapestOfIgnoresInapplicableStrategies(t *testing.T) {
	r := CheapestOf(
		WeightBased("oversize", []Tier{{MaxKg: 1, Base: 1, PerKg: 1}}), // fails
		Flat(500, "flat"),
	)
	q, err := r(context.Background(), pkg())
	if err != nil {
		t.Fatal(err)
	}
	if q.Strategy != "flat" {
		t.Fatalf("got %+v", q)
	}
}

func TestCheapestOfWithoutStrategiesErrors(t *testing.T) {
	if _, err := CheapestOf()(context.Background(), pkg()); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheapestOfAllFailSurfacesError(t *testing.T) {
	boom := errors.New("boom")
	r := CheapestOf(
		func(context.Context, Package) (RateQuote, error) { return RateQuote{}, boom },
		func(context.Context, Package) (RateQuote, error) { return RateQuote{}, boom },
	)
	if _, err := r(context.Background(), pkg()); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

// The context swaps strategies at runtime without a branch anywhere.
func TestShipperSwapsStrategyAtRuntime(t *testing.T) {
	s := NewShipper("acme", Flat(900, "flat"))
	ctx := context.Background()

	q, err := s.Quote(ctx, pkg())
	if err != nil || q.Total != 900 {
		t.Fatalf("%+v %v", q, err)
	}

	s.SetStrategy(Flat(300, "promo"))
	q, _ = s.Quote(ctx, pkg())
	if q.Total != 300 || q.Strategy != "promo" {
		t.Fatalf("swap failed: %+v", q)
	}
}

func TestShipperWithoutStrategyErrors(t *testing.T) {
	s := NewShipper("acme", nil)
	if _, err := s.Quote(context.Background(), pkg()); err == nil {
		t.Fatal("expected error")
	}
	if s.StrategyName() != "none" {
		t.Fatalf("got %q", s.StrategyName())
	}
}

func TestShipperWrapsStrategyErrors(t *testing.T) {
	boom := errors.New("boom")
	s := NewShipper("acme", func(context.Context, Package) (RateQuote, error) {
		return RateQuote{}, boom
	})
	_, err := s.Quote(context.Background(), pkg())
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "shipper acme") {
		t.Fatalf("missing context: %v", err)
	}
}

func TestCatalogueSelectsByName(t *testing.T) {
	c := NewCatalogue()
	c.Register("flat", Flat(500, "flat"))
	c.Register("distance", PerKm("d", 100, 5, 20))

	if len(c.Names()) != 2 {
		t.Fatalf("names=%v", c.Names())
	}
	r, err := c.Pick("flat")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := r(context.Background(), pkg())
	if q.Strategy != "flat" {
		t.Fatalf("got %+v", q)
	}
}

func TestCatalogueUnknownNameListsAvailable(t *testing.T) {
	c := NewCatalogue()
	c.Register("flat", Flat(500, "flat"))
	_, err := c.Pick("nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "flat") {
		t.Fatalf("error should list available strategies: %v", err)
	}
}

func TestMoneyString(t *testing.T) {
	cases := map[Money]string{
		0:    "0.00",
		5:    "0.05",
		125:  "1.25",
		-50:  "-0.50",
		1000: "10.00",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("%d: want %s, got %s", in, want, got)
		}
	}
}
