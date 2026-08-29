# Release notes — v1.7.0

A minor release about telling one database failure from another — and about
two places where Quark could not, on engines its own documentation
recommends.

## Added

- **`quark.IsUniqueViolation` and `quark.IsDeadlock`.** A handler that writes
  to the database needs to know whether a rejected insert was a duplicate it
  can explain to the user, or something it should not have swallowed. Until
  now the only exported signal was `ErrConstraintViolation`, which lumps
  unique, foreign-key, not-null and check violations into one value — enough
  to know the write was refused, not enough to answer with a `409` and name
  the field. The alternative was importing your driver and matching its error
  type by hand, which stops working the moment the application runs on a
  second engine.

  Both predicates match on the **error code the driver reports**, never on
  message text, so they are unaffected by the server's language and by
  wording changes between driver releases. Both walk the wrapping chain. See
  [Errors](https://jcsvwinston.github.io/quantum/quark/reference/api/errors) for when to reach for each.

## Fixed

- **PostgreSQL failures went unrecognised under `lib/pq`.** Quark classifies
  driver errors to decide three things on its own: whether to retry a
  transaction the engine chose as a deadlock victim, whether a duplicate link
  row can be ignored, and whether a read should fail over off an unreachable
  replica. That classification matched only the error type of `pgx`, while
  the installation guide prescribes `lib/pq` and the dialect accepts its
  driver names.

  On `lib/pq`, none of the three recognised anything — so `WithDeadlockRetry`
  never retried, and a downed read replica was never detected as one. Neither
  reported an error: the options were accepted, appeared active and did
  nothing. All three now read the code through the method both drivers
  expose, so either driver behaves the same.

- **On SQL Server, a rejected insert reported the wrong error entirely.**
  `Create` sends the `INSERT` and the identity lookup as a single batch, and
  when the server rejects the insert it still answers the lookup — with
  `NULL`. Reading that into a plain integer failed, and that conversion error
  was returned in place of the driver's, so a duplicate key arrived as a scan
  error naming no constraint, no table and no column. No rejected insert on
  SQL Server was diagnosable, by a person or by a program. The engine's own
  error now reaches the caller, and `ErrConstraintViolation` wraps it as it
  already did on the other engines.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>
