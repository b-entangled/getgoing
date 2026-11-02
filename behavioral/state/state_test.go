package state

import (
	"errors"
	"strings"
	"testing"
)

func machineWithStock() *Machine {
	return NewMachine(
		Item{SKU: "A1", Name: "Cola", Price: 100, Stock: 2},
		Item{SKU: "B2", Name: "Chips", Price: 50, Stock: 1},
	)
}

func TestStartsIdle(t *testing.T) {
	m := machineWithStock()
	if m.StateName() != "idle" {
		t.Fatalf("got %q", m.StateName())
	}
	if m.Credit() != 0 {
		t.Fatalf("credit=%d", m.Credit())
	}
}

func TestIdleRejectsSelection(t *testing.T) {
	m := machineWithStock()
	if err := m.Select("A1"); !errors.Is(err, ErrNoCredit) {
		t.Fatalf("want ErrNoCredit, got %v", err)
	}
}

func TestIdleRejectsRefund(t *testing.T) {
	m := machineWithStock()
	if err := m.Refund(); !errors.Is(err, ErrNothingToRefund) {
		t.Fatalf("want ErrNothingToRefund, got %v", err)
	}
}

// The state transition Idle → Credited is visible and automatic.
func TestCoinTransitionsToCredited(t *testing.T) {
	m := machineWithStock()
	if err := m.InsertCoin(Quarter); err != nil {
		t.Fatal(err)
	}
	if m.StateName() != "credited" {
		t.Fatalf("got %q", m.StateName())
	}
	if m.Credit() != 25 {
		t.Fatalf("credit=%d", m.Credit())
	}
}

func TestCreditAccumulatesAcrossCoins(t *testing.T) {
	m := machineWithStock()
	_ = m.InsertCoin(Quarter)
	_ = m.InsertCoin(Quarter)
	_ = m.InsertCoin(Dime)
	if m.Credit() != 60 {
		t.Fatalf("credit=%d", m.Credit())
	}
	if m.StateName() != "credited" {
		t.Fatalf("got %q", m.StateName())
	}
}

func TestInvalidCoinRejectedAndStateUnchanged(t *testing.T) {
	m := machineWithStock()
	if err := m.InsertCoin(Coin(7)); err == nil {
		t.Fatal("expected rejection of a non-standard coin")
	}
	if m.StateName() != "idle" || m.Credit() != 0 {
		t.Fatalf("state=%q credit=%d", m.StateName(), m.Credit())
	}
}

func TestSuccessfulPurchaseReturnsToIdle(t *testing.T) {
	m := machineWithStock()
	_ = m.InsertCoin(Quarter)
	_ = m.InsertCoin(Quarter)
	_ = m.InsertCoin(Dime)
	_ = m.InsertCoin(Dime) // 70

	if err := m.Select("A1"); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	// Failed purchase must keep the credit.
	if m.Credit() != 70 {
		t.Fatalf("credit lost: %d", m.Credit())
	}
	if m.StateName() != "credited" {
		t.Fatalf("state=%q", m.StateName())
	}

	_ = m.InsertCoin(Quarter)
	_ = m.InsertCoin(Dime) // 100
	if err := m.Select("A1"); err != nil {
		t.Fatal(err)
	}
	if m.StateName() != "idle" {
		t.Fatalf("want idle after purchase, got %q", m.StateName())
	}
	if m.Credit() != 0 {
		t.Fatalf("credit should be taken, got %d", m.Credit())
	}
	if got := m.History(); len(got) != 1 || got[0] != "A1" {
		t.Fatalf("history=%v", got)
	}
}

func TestRefundTransitionsToIdle(t *testing.T) {
	m := machineWithStock()
	_ = m.InsertCoin(Quarter)
	if err := m.Refund(); err != nil {
		t.Fatal(err)
	}
	if m.StateName() != "idle" || m.Credit() != 0 {
		t.Fatalf("state=%q credit=%d", m.StateName(), m.Credit())
	}
}

func TestUnknownSelectionRejected(t *testing.T) {
	m := machineWithStock()
	_ = m.InsertCoin(Quarter)
	if err := m.Select("ZZZ"); !errors.Is(err, ErrBadSelection) {
		t.Fatalf("want ErrBadSelection, got %v", err)
	}
}

func TestSoldOutItemRejected(t *testing.T) {
	m := NewMachine(Item{SKU: "A1", Name: "Cola", Price: 10, Stock: 0})
	_ = m.InsertCoin(Quarter)
	if err := m.Select("A1"); !errors.Is(err, ErrSoldOut) {
		t.Fatalf("want ErrSoldOut, got %v", err)
	}
}

// Buying the last item of the last product moves to SoldOut.
func TestLastSaleTransitionsToSoldOut(t *testing.T) {
	m := NewMachine(Item{SKU: "A1", Name: "Cola", Price: 10, Stock: 1})
	_ = m.InsertCoin(Quarter)
	if err := m.Select("A1"); err != nil {
		t.Fatal(err)
	}
	if m.StateName() != "sold-out" {
		t.Fatalf("want sold-out, got %q", m.StateName())
	}
}

func TestSoldOutRejectsCoinSoMoneyIsNotTrapped(t *testing.T) {
	m := NewMachine(Item{SKU: "A1", Name: "Cola", Price: 10, Stock: 1})
	_ = m.InsertCoin(Quarter)
	_ = m.Select("A1")

	if err := m.InsertCoin(Quarter); !errors.Is(err, ErrSoldOut) {
		t.Fatalf("want ErrSoldOut, got %v", err)
	}
	if m.Credit() != 0 {
		t.Fatalf("credit=%d", m.Credit())
	}
}

// Only the maintenance state offers restock — enforced by a checked
// assertion, not by an if/else chain at every call site.
func TestRestockRequiresMaintenance(t *testing.T) {
	m := machineWithStock()
	if err := m.Restock("A1", 5); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("want a maintenance error, got %v", err)
	}
}

func TestRestockInMaintenance(t *testing.T) {
	m := machineWithStock()
	if err := m.ToggleMaintenance(); err != nil {
		t.Fatal(err)
	}
	if m.StateName() != "maintenance" {
		t.Fatalf("got %q", m.StateName())
	}
	if err := m.Restock("A1", 5); err != nil {
		t.Fatal(err)
	}
	var cola Item
	for _, it := range m.Items() {
		if it.SKU == "A1" {
			cola = it
		}
	}
	if cola.Stock != 7 {
		t.Fatalf("stock=%d", cola.Stock)
	}
}

func TestMaintenanceBlocksSales(t *testing.T) {
	m := machineWithStock()
	_ = m.ToggleMaintenance()

	if err := m.InsertCoin(Quarter); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("want ErrMaintenance, got %v", err)
	}
	if err := m.Select("A1"); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("want ErrMaintenance, got %v", err)
	}
}

func TestMaintenanceRequiresRefundFirst(t *testing.T) {
	m := machineWithStock()
	_ = m.InsertCoin(Quarter)
	err := m.ToggleMaintenance()
	if err == nil {
		t.Fatal("entering maintenance with credit outstanding must fail")
	}
	if m.StateName() != "credited" {
		t.Fatalf("state changed on a refused transition: %q", m.StateName())
	}
	_ = m.Refund()
	if err := m.ToggleMaintenance(); err != nil {
		t.Fatalf("after refund it should be allowed: %v", err)
	}
}

func TestSoldOutCanOnlyExitViaMaintenance(t *testing.T) {
	m := NewMachine(Item{SKU: "A1", Name: "Cola", Price: 10, Stock: 1})
	_ = m.InsertCoin(Quarter)
	_ = m.Select("A1")
	if m.StateName() != "sold-out" {
		t.Fatalf("got %q", m.StateName())
	}

	// The only legal transition out.
	if err := m.ToggleMaintenance(); err != nil {
		t.Fatal(err)
	}
	if m.StateName() != "maintenance" {
		t.Fatalf("got %q", m.StateName())
	}
	if err := m.Restock("A1", 10); err != nil {
		t.Fatal(err)
	}
	_ = m.ToggleMaintenance()

	// Back in business.
	if err := m.InsertCoin(Quarter); err != nil {
		t.Fatalf("machine should accept coins again: %v", err)
	}
}

func TestTransitionLogIsRecorded(t *testing.T) {
	m := NewMachine(Item{SKU: "A1", Name: "Cola", Price: 10, Stock: 1})
	_ = m.InsertCoin(Quarter)
	_ = m.Select("A1")
	_ = m.ToggleMaintenance()

	want := []string{"start:idle", "to:credited", "to:sold-out", "to:maintenance"}
	got := m.Log()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("want %v\ngot  %v", want, got)
	}
}

// Every state answers every operation — there is no default/unknown branch
// anywhere in the context.
func TestEveryStateImplementsEveryOperation(t *testing.T) {
	states := []State{&Idle{}, &Credited{}, &Maintenance{}, &SoldOut{}}
	for _, s := range states {
		if s.Name() == "" {
			t.Fatal("state with empty name")
		}
		m := NewMachine()
		// These must all return errors rather than panic or misbehave.
		_ = s.InsertCoin(m, Quarter)
		_ = s.Select(m, "nope")
		_ = s.Refund(m)
		_ = s.ToggleMaintenance(m)
	}
}
