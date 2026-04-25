package transport

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Attempt is one strategy on the degradation ladder: a labelled, quality-ranked
// dial function.
type Attempt struct {
	Label   string
	Quality QualityHint
	Dial    func(ctx context.Context) (Conn, error)
}

// Result reports the outcome of a single attempt, for observable degradation.
type Result struct {
	Label   string
	Quality QualityHint
	OK      bool
	RTT     time.Duration
	Err     error
}

// DefaultStagger is the delay between successive happy-eyeballs launches.
const DefaultStagger = 250 * time.Millisecond

type outcome struct {
	conn  Conn
	label string
	res   Result
}

// Race runs the attempts with happy-eyeballs: it sorts them by quality, starts
// them in parallel with a stagger, and returns the first connection to come up,
// cancelling the rest. Every settled attempt is delivered to onResult (which
// may be nil), so callers can render the degradation sequence as it happens.
//
// The returned Conn belongs to the winning attempt; its label is returned too.
func Race(ctx context.Context, attempts []Attempt, stagger time.Duration, onResult func(Result)) (Conn, string, error) {
	if len(attempts) == 0 {
		return nil, "", fmt.Errorf("transport: no connection strategies to try")
	}
	ordered := append([]Attempt(nil), attempts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Quality < ordered[j].Quality })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan outcome, len(ordered))
	for i, a := range ordered {
		go func(i int, a Attempt) {
			// Stagger launches so the best-quality attempt gets a head start,
			// without serialising: a later attempt still fires if earlier ones
			// are slow.
			select {
			case <-ctx.Done():
				results <- outcome{res: Result{Label: a.Label, Quality: a.Quality, Err: ctx.Err()}}
				return
			case <-time.After(time.Duration(i) * stagger):
			}
			start := time.Now()
			conn, err := a.Dial(ctx)
			results <- outcome{
				conn:  conn,
				label: a.Label,
				res:   Result{Label: a.Label, Quality: a.Quality, RTT: time.Since(start), OK: err == nil, Err: err},
			}
		}(i, a)
	}

	var lastErr error
	for i := 0; i < len(ordered); i++ {
		o := <-results
		if onResult != nil && (o.res.OK || o.res.Err != nil) {
			onResult(o.res)
		}
		if o.res.OK && o.conn != nil {
			cancel() // stand down the remaining attempts
			// Close any straggler that still establishes after we picked a winner.
			go drain(results, len(ordered)-i-1)
			return o.conn, o.label, nil
		}
		if o.res.Err != nil {
			lastErr = o.res.Err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("transport: all strategies failed")
	}
	return nil, "", lastErr
}

// drain consumes the n outstanding outcomes after a winner is chosen, closing
// any connection that raced to establish before cancellation landed.
func drain(results <-chan outcome, n int) {
	for i := 0; i < n; i++ {
		o := <-results
		if o.conn != nil {
			o.conn.Close("superseded by faster route")
		}
	}
}
