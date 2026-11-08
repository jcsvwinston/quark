// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package readreplicas is the code of the guide "Read replicas in
// production": a client whose reads go to replicas and whose writes go to
// the primary, the reads that must see their own writes, the transaction
// that never leaves the primary, and a count of what could route.
package readreplicas

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jcsvwinston/quark"

	// The driver module: registers the database/sql driver and the
	// classifier that tells a connection failure from a query error — what
	// decides whether a failed replica read fails over to the primary.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)

// Order is the row this service writes once and reads many times.
type Order struct {
	ID       int64  `db:"id" pk:"true"`
	Customer string `db:"customer" quark:"not_null"`
	Total    int64  `db:"total_cents"`
	Status   string `db:"status"`
}

// Open gives the service one client: the primary for writes, the replicas
// for reads, each read going to the replica with the fewest connections in
// use. New opens and pings every replica; one that does not answer is an
// error, not a replica skipped, so the service does not start on a broken
// topology. A replica that fails later is skipped for the cooldown.
func Open(primaryDSN string, replicaDSNs ...string) (*quark.Client, error) {
	return quark.New("sqlite", primaryDSN,
		quark.WithReplicas(replicaDSNs...),
		quark.WithReplicaStrategy(quark.ReplicaLeastConn),
		quark.WithReplicaDownCooldown(10*time.Second),
		quark.WithMaxOpenConns(8),
	)
}

// Place writes the order. Writes never route: the INSERT runs on the
// primary, and so does the read of the key it generates.
func Place(ctx context.Context, client *quark.Client, o *Order) error {
	return quark.For[Order](ctx, client).Create(o)
}

// Confirmation is the page shown right after Place. It must see the row
// that was just written, so it pins the read to the primary: a replica may
// not have it yet. Sticky marks this read only; the next query built on
// the plain context routes again.
func Confirmation(ctx context.Context, client *quark.Client, id int64) (Order, error) {
	return quark.For[Order](quark.Sticky(ctx), client).Find(id)
}

// History is the customer's order list. It tolerates replication lag — an
// order placed a moment ago may be missing from it — so it reads from a
// replica.
func History(ctx context.Context, client *quark.Client, customer string) ([]Order, error) {
	return quark.For[Order](ctx, client).
		Where("customer", "=", customer).
		OrderBy("id", "DESC").
		Limit(50).
		List()
}

// Settle reads and writes in one transaction. Every statement inside it
// runs on the transaction's connection, which is on the primary, so the
// reads see the write without Sticky.
func Settle(ctx context.Context, client *quark.Client, id int64) (Order, error) {
	var settled Order
	err := client.Tx(ctx, func(tx *quark.Tx) error {
		o, err := quark.ForTx[Order](ctx, tx).Find(id)
		if err != nil {
			return err
		}
		o.Status = "settled"
		if _, err := quark.ForTx[Order](ctx, tx).Update(&o); err != nil {
			return err
		}
		settled, err = quark.ForTx[Order](ctx, tx).Find(id)
		return err
	})
	return settled, err
}

// Split counts the statements Quark reports: the SELECTs that could have
// routed to a replica and the writes that never do. Quark reports every
// statement to its observers but not the pool that ran it, so this is the
// most the client can tell; which replica served what is read on the
// servers.
type Split struct{ Reads, Writes atomic.Int64 }

func (s *Split) ObserveQuery(e quark.QueryEvent) {
	switch e.Operation {
	case "SELECT", "SELECT (stream)", "SELECT (cursor)", "PRELOAD":
		s.Reads.Add(1)
	case "EXEC":
		s.Writes.Add(1)
	}
}
