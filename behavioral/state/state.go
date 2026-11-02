// Package state demonstrates the State pattern.
//
// State lets an object alter its behaviour when its internal state changes —
// the object appears to change class. In Go this is a struct with an
// interface field; each state implements that interface and the object
// delegates to it. Transitions are performed by the states themselves
// through the context object.
//
// Why not a big switch? A switch on state scales quadratically: every
// handler must know every state. With State, each state only knows its own
// rules, illegal combinations become impossible to write, and adding a state
// is one type plus one constructor.
//
// Domain: a vending machine (Idle / Credited / Maintenance).
package state

import (
	"errors"
	"fmt"
	"sort"
)

// --- domain errors, matched with errors.Is ---

var (
	ErrNoCredit          = errors.New("state: insert a coin first")
	ErrNothingToRefund   = errors.New("state: nothing to refund")
	ErrInsufficientFunds = errors.New("state: insufficient funds")
	ErrSoldOut           = errors.New("state: item sold out")
	ErrMaintenance       = errors.New("state: machine under maintenance")
	ErrBadSelection      = errors.New("state: no such selection")
)

// Coin denominations in cents.
type Coin int

// Supported coins.
const (
	Nickel  Coin = 5
	Dime    Coin = 10
	Quarter Coin = 25
)

// Item is a stocked product.
type Item struct {
	SKU   string
	Name  string
	Price int // cents
	Stock int
}

// --- the context ---

// State is what the machine currently behaves like. Every method takes the
// machine so the state can transition it.
type State interface {
	Name() string
	InsertCoin(m *Machine, c Coin) error
	Select(m *Machine, sku string) error
	Refund(m *Machine) error
	// Maintenance allows service-mode operations; ordinary states refuse.
	ToggleMaintenance(m *Machine) error
}

// Machine is the context: it owns data and delegates behaviour to m.state.
// The state field is unexported, so only this package can transition it —
// an invariant the GoF version leaves to convention.
type Machine struct {
	state   State
	credit  int
	stock   map[string]*Item
	log     []string
	history []string
}

// NewMachine builds a stocked machine in its Idle state.
func NewMachine(items ...Item) *Machine {
	m := &Machine{stock: make(map[string]*Item, len(items))}
	for i := range items {
		it := items[i]
		m.stock[it.SKU] = &it
	}
	m.state = &Idle{}
	m.log = append(m.log, "start:idle")
	return m
}

// --- delegation: one method per operation, no switch anywhere ---

// InsertCoin accepts money.
func (m *Machine) InsertCoin(c Coin) error { return m.state.InsertCoin(m, c) }

// Select dispenses an item.
func (m *Machine) Select(sku string) error { return m.state.Select(m, sku) }

// Refund returns the credit.
func (m *Machine) Refund() error { return m.state.Refund(m) }

// ToggleMaintenance enters or leaves service mode.
func (m *Machine) ToggleMaintenance() error { return m.state.ToggleMaintenance(m) }

// Credit reports the current credit in cents.
func (m *Machine) Credit() int { return m.credit }

// StateName reports the current state for logging and tests.
func (m *Machine) StateName() string { return m.state.Name() }

// Log returns the transition log.
func (m *Machine) Log() []string { return append([]string(nil), m.log...) }

// Items returns the stock levels sorted by SKU.
func (m *Machine) Items() []Item {
	out := make([]Item, 0, len(m.stock))
	for _, it := range m.stock {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SKU < out[j].SKU })
	return out
}

// transition moves to a new state and records it. Only states call this.
func (m *Machine) transition(to State) {
	m.state = to
	m.log = append(m.log, "to:"+to.Name())
}

// creditCoin adds to the running credit.
func (m *Machine) creditCoin(c Coin) { m.credit += int(c) }

// takeCredit clears and returns the credit.
func (m *Machine) takeCredit() int {
	c := m.credit
	m.credit = 0
	return c
}

// record appends to the dispense history.
func (m *Machine) record(sku string) {
	m.history = append(m.history, sku)
}

// History returns the list of dispensed SKUs.
func (m *Machine) History() []string { return append([]string(nil), m.history...) }

// --- state A: Idle ---

// Idle has no credit. It is the state a freshly-cashiered machine sits in.
type Idle struct{}

// Name identifies the state.
func (Idle) Name() string { return "idle" }

// InsertCoin accepts money and transitions to Credited.
func (Idle) InsertCoin(m *Machine, c Coin) error {
	if !validCoin(c) {
		return fmt.Errorf("state: rejected coin of %d", c)
	}
	m.creditCoin(c)
	m.transition(&Credited{})
	return nil
}

// Select is illegal with no credit — the machine *is* idle.
func (Idle) Select(*Machine, string) error { return ErrNoCredit }

// Refund has nothing to return.
func (Idle) Refund(*Machine) error { return ErrNothingToRefund }

// ToggleMaintenance moves Idle → Maintenance.
func (Idle) ToggleMaintenance(m *Machine) error {
	m.transition(&Maintenance{})
	return nil
}

// --- state B: Credited ---

// Credited has money in the machine.
type Credited struct{}

// Name identifies the state.
func (Credited) Name() string { return "credited" }

// InsertCoin accumulates more credit.
func (Credited) InsertCoin(m *Machine, c Coin) error {
	if !validCoin(c) {
		return fmt.Errorf("state: rejected coin of %d", c)
	}
	m.creditCoin(c)
	return nil
}

// Select dispenses if the credit covers the price; otherwise it refuses and
// *keeps* the credit (refunding on failure would be hostile).
// On success it returns to Idle — unless the machine is now empty.
func (Credited) Select(m *Machine, sku string) error {
	it, ok := m.stock[sku]
	if !ok {
		return fmt.Errorf("%w: %q", ErrBadSelection, sku)
	}
	if it.Stock == 0 {
		return fmt.Errorf("%w: %s", ErrSoldOut, it.Name)
	}
	if m.Credit() < it.Price {
		return fmt.Errorf("%w: have %d need %d", ErrInsufficientFunds, m.Credit(), it.Price)
	}

	m.takeCredit()
	it.Stock--
	m.record(sku)

	if it.Stock == 0 && m.empty() {
		m.transition(&SoldOut{})
		return nil
	}
	m.transition(&Idle{})
	return nil
}

// Refund returns the credit and goes back to Idle.
func (Credited) Refund(m *Machine) error {
	m.takeCredit()
	m.transition(&Idle{})
	return nil
}

// ToggleMaintenance refuses: credit is at stake, force a refund first.
func (Credited) ToggleMaintenance(*Machine) error {
	return errors.New("state: refund credit before entering maintenance")
}

// --- state C: Maintenance ---

// Maintenance is service mode: no sales, but stock can be restocked.
// Exiting always returns to Idle, because restock implies the machine is
// sellable again — the transition is owned by this state.
type Maintenance struct{}

// Name identifies the state.
func (Maintenance) Name() string { return "maintenance" }

// InsertCoin is refused: the coin mech is locked out.
func (Maintenance) InsertCoin(*Machine, Coin) error { return ErrMaintenance }

// Select is refused.
func (Maintenance) Select(*Machine, string) error { return ErrMaintenance }

// Refund has nothing to give.
func (Maintenance) Refund(*Machine) error { return ErrNothingToRefund }

// ToggleMaintenance returns to Idle.
func (Maintenance) ToggleMaintenance(m *Machine) error {
	m.transition(&Idle{})
	return nil
}

// Restock is an operation *only* Maintenance has — this is the concrete
// benefit of the pattern: the method exists on the maintenance state and
// simply is not part of the ordinary State interface surface, so a machine
// in Idle has no way to be asked to restock through the state object.
func (Maintenance) Restock(m *Machine, sku string, n int) error {
	it, ok := m.stock[sku]
	if !ok {
		return fmt.Errorf("%w: %q", ErrBadSelection, sku)
	}
	it.Stock += n
	m.log = append(m.log, "restock:"+sku)
	return nil
}

// --- state D: SoldOut ---

// SoldOut is terminal until restocked (which requires Maintenance).
type SoldOut struct{}

// Name identifies the state.
func (SoldOut) Name() string { return "sold-out" }

// InsertCoin is refused so a customer's money is not trapped.
func (SoldOut) InsertCoin(*Machine, Coin) error { return ErrSoldOut }

// Select is refused.
func (SoldOut) Select(*Machine, string) error { return ErrSoldOut }

// Refund has nothing to give.
func (SoldOut) Refund(*Machine) error { return ErrNothingToRefund }

// ToggleMaintenance is the only legal transition out of SoldOut: a
// technician must service the machine.
func (SoldOut) ToggleMaintenance(m *Machine) error {
	m.transition(&Maintenance{})
	return nil
}

// helpers

func validCoin(c Coin) bool {
	switch c {
	case Nickel, Dime, Quarter:
		return true
	default:
		return false
	}
}

func (m *Machine) empty() bool {
	for _, it := range m.stock {
		if it.Stock > 0 {
			return false
		}
	}
	return true
}

// Restock lets a caller restock only when the machine is in Maintenance —
// it performs a checked type assertion, demonstrating that operations can
// be gated on the current state without any if/else chain in the caller.
func (m *Machine) Restock(sku string, n int) error {
	ms, ok := m.state.(*Maintenance)
	if !ok {
		return fmt.Errorf("state: restock requires maintenance mode, currently %q", m.StateName())
	}
	return ms.Restock(m, sku, n)
}
