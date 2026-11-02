// Package strategy demonstrates the Strategy pattern.
//
// Strategy defines a family of interchangeable algorithms, encapsulates each
// one, and makes them selectable at runtime.
//
// In Go this is the highest-value pattern in the whole catalogue, because
// functions are first class: the "interface + N implementations + factory"
// machinery of Java collapses to a single func type plus closures for
// captured configuration. There is no Strategy interface hierarchy to grow,
// no subclassing, and swapping a strategy in a test is one line.
//
// Domain: shipping-rate calculation.
package strategy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Money is an amount in the smallest currency unit (cents).
type Money int64

// String renders as a decimal amount.
func (m Money) String() string {
	neg := m < 0
	if neg {
		m = -m
	}
	s := fmt.Sprintf("%d.%02d", m/100, m%100)
	if neg {
		return "-" + s
	}
	return s
}

// Package is the input to every rate strategy.
type Package struct {
	WeightKg   float64
	DistanceKm float64
	ValueCents Money
	DestZone   string
}

// Validate checks the package is well-formed.
func (p Package) Validate() error {
	if p.WeightKg <= 0 {
		return errors.New("strategy: weight must be positive")
	}
	if p.DistanceKm < 0 {
		return errors.New("strategy: distance must not be negative")
	}
	return nil
}

// RateQuote is the strategy's output.
type RateQuote struct {
	Strategy string
	Total    Money
	Breaks   []string // human-readable explanation of each adjustment
}

// --- the strategy type: a plain function ---

// Rate computes a quote for a package. This one-line type *is* the
// strategy interface. Configured variants are closures.
type Rate func(ctx context.Context, p Package) (RateQuote, error)

// --- strategy A: flat rate ---

// Flat returns a fixed price regardless of the package.
func Flat(price Money, name string) Rate {
	return func(ctx context.Context, p Package) (RateQuote, error) {
		if err := ctx.Err(); err != nil {
			return RateQuote{}, err
		}
		if err := p.Validate(); err != nil {
			return RateQuote{}, err
		}
		return RateQuote{
			Strategy: name,
			Total:    price,
			Breaks:   []string{fmt.Sprintf("flat %s", price)},
		}, nil
	}
}

// --- strategy B: weight-based tiering ---

// Tier is a weight bracket.
type Tier struct {
	MaxKg float64
	Base  Money
	PerKg Money
}

// WeightBased charges by bracket plus a per-kilo rate. The tiers are data,
// not code — adding a bracket does not add a type.
func WeightBased(name string, tiers []Tier) Rate {
	return func(ctx context.Context, p Package) (RateQuote, error) {
		if err := ctx.Err(); err != nil {
			return RateQuote{}, err
		}
		if err := p.Validate(); err != nil {
			return RateQuote{}, err
		}
		for _, t := range tiers {
			if p.WeightKg > t.MaxKg {
				continue
			}
			total := t.Base + Money(math.Ceil(p.WeightKg*float64(t.PerKg)))
			return RateQuote{
				Strategy: name,
				Total:    total,
				Breaks: []string{
					fmt.Sprintf("tier ≤%.1fkg base %s", t.MaxKg, t.Base),
					fmt.Sprintf("%.1fkg × %s", p.WeightKg, t.PerKg),
				},
			}, nil
		}
		return RateQuote{}, fmt.Errorf("strategy: %.1fkg exceeds all tiers", p.WeightKg)
	}
}

// --- strategy C: distance-based ---

// PerKm charges a base fee plus a rate per kilometre, with a free radius.
func PerKm(name string, base Money, perKm Money, freeRadiusKm float64) Rate {
	return func(ctx context.Context, p Package) (RateQuote, error) {
		if err := ctx.Err(); err != nil {
			return RateQuote{}, err
		}
		if err := p.Validate(); err != nil {
			return RateQuote{}, err
		}
		billable := math.Max(0, p.DistanceKm-freeRadiusKm)
		total := base + Money(math.Ceil(billable*float64(perKm)))
		return RateQuote{
			Strategy: name,
			Total:    total,
			Breaks: []string{
				fmt.Sprintf("base %s", base),
				fmt.Sprintf("%.0fkm − %.0fkm free = %.0fkm × %s",
					p.DistanceKm, freeRadiusKm, billable, perKm),
			},
		}, nil
	}
}

// --- strategy D: insured value ---

// InsuredValue prices as a percentage of declared value, with a minimum.
func InsuredValue(name string, pct float64, minimum Money) Rate {
	return func(ctx context.Context, p Package) (RateQuote, error) {
		if err := ctx.Err(); err != nil {
			return RateQuote{}, err
		}
		if err := p.Validate(); err != nil {
			return RateQuote{}, err
		}
		computed := Money(math.Ceil(float64(p.ValueCents) * pct))
		total := max(computed, minimum)
		return RateQuote{
			Strategy: name,
			Total:    total,
			Breaks: []string{
				fmt.Sprintf("%.1f%% of %s = %s", pct*100, p.ValueCents, computed),
				fmt.Sprintf("minimum %s applied: %t", minimum, total == minimum && computed < minimum),
			},
		}, nil
	}
}

// --- composition: decorators over strategies ---

// WithSurcharge wraps a Rate and adds a percentage uplift. Because Rate is
// just a func, a "decorated strategy" needs no wrapper type at all.
func WithSurcharge(inner Rate, pct float64, label string) Rate {
	return func(ctx context.Context, p Package) (RateQuote, error) {
		q, err := inner(ctx, p)
		if err != nil {
			return RateQuote{}, err
		}
		extra := Money(math.Ceil(float64(q.Total) * pct))
		q.Total += extra
		q.Breaks = append(q.Breaks, fmt.Sprintf("%s +%s", label, extra))
		return q, nil
	}
}

// CheapestOf selects the minimum among several strategies at runtime —
// the reason strategies exist. Ties break on strategy name for determinism.
func CheapestOf(rates ...Rate) Rate {
	return func(ctx context.Context, p Package) (RateQuote, error) {
		if len(rates) == 0 {
			return RateQuote{}, errors.New("strategy: no rates to compare")
		}
		var best RateQuote
		var bestErr error
		for i, r := range rates {
			q, err := r(ctx, p)
			if err != nil {
				// Remember the error but keep comparing: one strategy being
				// inapplicable must not veto the others.
				if bestErr == nil {
					bestErr = fmt.Errorf("rate %d: %w", i, err)
				}
				continue
			}
			if best.Strategy == "" || q.Total < best.Total ||
				(q.Total == best.Total && q.Strategy < best.Strategy) {
				best = q
				bestErr = nil
			}
		}
		if best.Strategy == "" {
			return RateQuote{}, bestErr
		}
		best.Breaks = append(best.Breaks, "chosen as cheapest")
		return best, nil
	}
}

// --- the context ---

// Shipper is the context: it holds a strategy and delegates to it.
// The strategy field is settable at runtime — that is the whole point.
type Shipper struct {
	strategy Rate
	fallback Rate
	name     string
}

// NewShipper configures a shipper with its default strategy.
func NewShipper(name string, r Rate) *Shipper {
	return &Shipper{name: name, strategy: r}
}

// SetStrategy swaps the algorithm, e.g. when a promo starts.
func (s *Shipper) SetStrategy(r Rate) { s.strategy = r }

// StrategyName reports the current strategy for logging.
func (s *Shipper) StrategyName() string {
	if s.strategy == nil {
		return "none"
	}
	// Strategies record their own name in the quote; use a probe when idle.
	q, err := s.strategy(context.Background(), Package{WeightKg: 1, DistanceKm: 1, ValueCents: 1})
	if err == nil {
		return q.Strategy
	}
	return s.name
}

// Quote delegates to the current strategy.
func (s *Shipper) Quote(ctx context.Context, p Package) (RateQuote, error) {
	if s.strategy == nil {
		return RateQuote{}, errors.New("strategy: no rate strategy configured")
	}
	q, err := s.strategy(ctx, p)
	if err != nil {
		return RateQuote{}, fmt.Errorf("shipper %s: %w", s.name, err)
	}
	return q, nil
}

// Catalogue lists named strategies so an operator can pick one from config.
type Catalogue struct {
	rates map[string]Rate
}

// NewCatalogue builds an empty catalogue.
func NewCatalogue() *Catalogue { return &Catalogue{rates: make(map[string]Rate)} }

// Register adds a named strategy. Duplicate names replace the old one —
// unlike creational registration, re-pricing a strategy at runtime is a
// feature, not a programming error.
func (c *Catalogue) Register(name string, r Rate) { c.rates[name] = r }

// Pick returns a named strategy, or an error listing what is available.
func (c *Catalogue) Pick(name string) (Rate, error) {
	r, ok := c.rates[name]
	if !ok {
		return nil, fmt.Errorf("strategy: unknown %q (have %s)", name, strings.Join(c.Names(), ","))
	}
	return r, nil
}

// Names lists registered strategies, sorted.
func (c *Catalogue) Names() []string {
	out := make([]string, 0, len(c.rates))
	for k := range c.rates {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
