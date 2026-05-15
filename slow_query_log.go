// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

// Slow-query logging (F4-3).
//
// Every Quark operation already feeds a QueryEvent into the observer
// pipeline (notifyObservers, query_builder.go). Slow-query logging
// piggybacks on that signal: when WithSlowQueryThreshold is configured
// and an operation's duration crosses the threshold, the Client emits a
// structured WARN through its slog logger before invoking the observer
// callbacks.
//
// The log line carries duration / threshold / operation / table / rows
// / sql. Bind arguments are NOT emitted — the parameterised SQL is the
// observable surface, the same redaction principle as F4-2 spans. A
// caller that wants args in their pipeline can register their own
// QueryObserver and format them under their own retention policy.

// logSlowQueryIfNeeded emits one structured WARN log line for an
// operation that exceeded WithSlowQueryThreshold. The check is a single
// cheap comparison: a Client with the feature disabled (threshold <= 0)
// returns immediately and pays nothing.
func (c *Client) logSlowQueryIfNeeded(e QueryEvent) {
	if c.slowQueryThreshold <= 0 || e.Duration < c.slowQueryThreshold {
		return
	}
	if c.logger == nil {
		return
	}
	// e.Args is intentionally omitted — see the F4-2 redaction principle:
	// a structured log line must not surface bind values it has no
	// authority to retain. Callers that want args in their pipeline
	// should register their own QueryObserver and apply their own
	// retention policy.
	c.logger.Warn(
		"slow query",
		"duration_ms", e.Duration.Milliseconds(),
		"threshold_ms", c.slowQueryThreshold.Milliseconds(),
		"operation", e.Operation,
		"table", e.Table,
		"rows", e.Rows,
		"sql", e.SQL,
	)
}
