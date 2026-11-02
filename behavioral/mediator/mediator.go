// Package mediator demonstrates the Mediator pattern.
//
// Mediator centralises communication so components never reference each
// other. The GoF version — a Mediator interface plus Colleague objects
// holding back-pointers — becomes a lock-ordering nightmare the moment two
// colleagues call each other. Go's answer is a single goroutine owning a
// channel: coordination state has exactly one owner, so data races are
// logically impossible rather than merely prevented by mutexes.
//
// Two shapes are shown:
//   - EventLoop: a typed command channel dispatched by one goroutine;
//   - Hub: a many-to-many mediator where components publish facts and
//     subscribe to reactions, still with no component-to-component links.
package mediator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// =====================================================================
// Shape 1: single event loop (strong ownership)
// =====================================================================

// Command is a fact submitted to the loop. Implementations are small structs
// carrying only data — no callbacks, so the loop stays in control of order.
type Command interface {
	// Type names the command for logging.
	Type() string
}

// MoveVehicle asks the planner to recompute a route.
type MoveVehicle struct {
	Vehicle  string
	Position int
}

// Type names the command.
func (MoveVehicle) Type() string { return "move" }

// FuelLow reports a vehicle below its threshold.
type FuelLow struct {
	Vehicle string
	Percent int
}

// Type names the command.
func (FuelLow) Type() string { return "fuel" }

// Emit records an event produced by the loop, for observation in tests.
type Emit struct {
	Topic string
	Text  string
}

// Type names the command.
func (Emit) Type() string { return "emit" }

// Route is what the planner produces.
type Route struct {
	Vehicle string
	Steps   []string
}

// EventLoop is the mediator: one goroutine, one channel, one owner.
type EventLoop struct {
	in  chan Command
	out chan Event

	mu     sync.RWMutex
	routes map[string]Route
	log    []string

	// handlers maps a command type to the reaction. Reactions may publish
	// further commands, but only via Publish, never by reaching into state.
	handlers map[string]func(Command)
}

// Event is something the loop tells the outside world.
type Event struct {
	Topic string
	Text  string
	At    time.Time
}

// NewEventLoop builds a loop with the given buffer size. A buffer is
// required: publishing from inside a handler would otherwise deadlock.
func NewEventLoop(buffer int) *EventLoop {
	if buffer < 1 {
		buffer = 16
	}
	e := &EventLoop{
		in:       make(chan Command, buffer),
		out:      make(chan Event, buffer),
		routes:   make(map[string]Route),
		handlers: make(map[string]func(Command)),
	}
	e.on("move", e.handleMove)
	e.on("fuel", e.handleFuel)
	return e
}

// on registers a reaction for a command type. Must be called before Run.
func (e *EventLoop) on(typ string, fn func(Command)) {
	e.handlers[typ] = fn
}

// Publish submits a command. Safe from any goroutine. It never blocks while
// the context is alive; if the loop has stopped, the command is dropped.
func (e *EventLoop) Publish(ctx context.Context, c Command) error {
	select {
	case e.in <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Events exposes the outbound stream. Callers range over it until the loop
// closes it on shutdown.
func (e *EventLoop) Events() <-chan Event { return e.out }

// Run is the single event loop. Only this goroutine touches e.routes and
// e.log without a lock — and after Run returns, nothing does.
// Run returns when ctx is cancelled or the input channel is closed.
func (e *EventLoop) Run(ctx context.Context) {
	defer close(e.out)
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-e.in:
			if !ok {
				return
			}
			e.dispatch(ctx, c)
		}
	}
}

// dispatch looks up and runs a reaction, recording it in the log.
func (e *EventLoop) dispatch(ctx context.Context, c Command) {
	e.mu.Lock()
	e.log = append(e.log, c.Type())
	e.mu.Unlock()

	fn, ok := e.handlers[c.Type()]
	if !ok {
		return
	}
	fn(c)

	_ = ctx
}

func (e *EventLoop) handleMove(c Command) {
	m := c.(MoveVehicle)
	e.mu.Lock()
	e.routes[m.Vehicle] = Route{
		Vehicle: m.Vehicle,
		Steps:   []string{fmt.Sprintf("at %d", m.Position)},
	}
	e.mu.Unlock()
	e.emit("route", fmt.Sprintf("%s -> %d", m.Vehicle, m.Position))
}

func (e *EventLoop) handleFuel(c Command) {
	f := c.(FuelLow)
	if f.Percent > 25 {
		return // not low enough to act on
	}
	e.emit("refuel", fmt.Sprintf("%s at %d%%", f.Vehicle, f.Percent))
}

// emit publishes an outbound event without blocking. Safe from the loop
// goroutine because the channel is buffered.
func (e *EventLoop) emit(topic, text string) {
	select {
	case e.out <- Event{Topic: topic, Text: text, At: time.Now()}:
	default: // never block the loop on a slow observer
	}
}

// RouteOf reads the loop's state from outside — for tests and diagnostics.
func (e *EventLoop) RouteOf(v string) (Route, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r, ok := e.routes[v]
	return r, ok
}

// Log returns the ordered list of command types handled.
func (e *EventLoop) Log() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.log...)
}

// =====================================================================
// Shape 2: hub (many-to-many, components still decoupled)
// =====================================================================

// Handler reacts to a hub fact.
type Handler func(f Fact)

// Fact is a named payload published to the hub.
type Fact struct {
	Topic   string
	Payload string
}

// Hub is a mediator that fans facts out to subscribers. Components publish
// to the hub and subscribe to topics; they never hold a reference to each
// other, so there is no cycle to deadlock.
type Hub struct {
	mu      sync.RWMutex
	subs    map[string][]subscription
	closed  bool
	dropped atomicCounter
}

type subscription struct {
	topic string
	ch    chan Fact
}

// NewHub builds an empty hub.
func NewHub() *Hub { return &Hub{subs: make(map[string][]subscription)} }

// Subscribe returns a channel of facts for a topic and a cancel func.
// The channel is closed when cancelled or when the hub is closed.
func (h *Hub) Subscribe(topic string, buffer int) (<-chan Fact, func()) {
	if buffer < 1 {
		buffer = 8
	}
	ch := make(chan Fact, buffer)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subs[topic] = append(h.subs[topic], subscription{topic: topic, ch: ch})
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() { h.unsubscribe(topic, ch) })
	}
	return ch, cancel
}

// Publish fans a fact out to every subscriber of its topic. It never blocks:
// a subscriber that is not draining its channel loses that fact rather than
// stalling the publisher. Dropped facts are counted so the loss is visible.
func (h *Hub) Publish(f Fact) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return 0
	}
	delivered := 0
	for _, s := range h.subs[f.Topic] {
		select {
		case s.ch <- f:
			delivered++
		default:
			h.dropped.inc()
		}
	}
	return delivered
}

// unsubscribe removes and closes a channel. Caller must not hold h.mu.
func (h *Hub) unsubscribe(topic string, ch chan Fact) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.subs[topic]
	for i, s := range list {
		if s.ch == ch {
			h.subs[topic] = append(list[:i], list[i+1:]...)
			close(ch)
			return
		}
	}
}

// Close shuts the hub down and closes every subscriber channel, so
// `for f := range ch` terminates for every consumer.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, list := range h.subs {
		for _, s := range list {
			close(s.ch)
		}
	}
	h.subs = nil
}

// Dropped reports how many facts were discarded for slow subscribers.
func (h *Hub) Dropped() int { return int(h.dropped.get()) }

// Topics lists topics that currently have at least one subscriber.
func (h *Hub) Topics() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.subs))
	for t := range h.subs {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// atomicCounter is a tiny non-blocking counter; kept local to avoid an
// external dependency for one field.
type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *atomicCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// ErrHubClosed is returned by Publish on a closed hub's peers.
var ErrHubClosed = errors.New("mediator: hub closed")
