// Package channels demonstrates the semantics of Go's channels:
//
//   - An **unbuffered** channel is a rendezvous: a send completes only
//     when a receive takes the value, and vice versa.
//   - A **buffered** channel decouples the two until the buffer fills;
//     `len(ch)` reports how many values are waiting.
//   - **close** is how the sender says "no more values": receives then
//     yield the zero value with ok == false, and `range` terminates.
//   - A **nil** channel blocks forever — in a select it is simply never
//     ready, which is a feature: it lets you switch a case off.
//   - **select** waits for whichever case is ready first and can take a
//     `default` to poll without blocking.
//
// Domain: handing jobs from a producer to a consumer.
package channels

// Produce sends every job on ch (blocking per value on an unbuffered
// channel) and returns once all are sent. The **sender owns close**: the
// producer is the only side that ever closes, so no send-on-closed panic
// is possible.
func Produce(jobs []int, ch chan<- int) {
	defer close(ch)
	for _, j := range jobs {
		ch <- j
	}
}

// Consume drains ch until it is closed and returns everything received.
// Receiving from a closed channel never blocks, so the loop ends.
func Consume(ch <-chan int) []int {
	var out []int
	for v := range ch {
		out = append(out, v)
	}
	return out
}

// TryReceive reports whether a value was waiting, without blocking: the
// default branch runs when nothing is ready.
func TryReceive(ch <-chan int) (int, bool) {
	select {
	case v, ok := <-ch:
		return v, ok
	default:
		return 0, false
	}
}

// Ready reports whether ch would accept a receive right now.
func Ready(ch <-chan int) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
