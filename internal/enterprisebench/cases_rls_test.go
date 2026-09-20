// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

// controlsRls is the tenancy family: what Quark can do when several customers
// share one database. Every title here says only what its probe reads — the
// emitted statement, the returned rows, the recorded log line — because a
// title that promises more than the probe checks is a control that passes
// forever while the capability underneath it rots.
//
// Native row-level security is PostgreSQL's, and this bench has no live
// PostgreSQL. What a SQLite-backed bench CAN read is the client-side half of
// it: which dialects the router accepts, which session variable it sets, what
// the policy generator renders, how the cache is keyed, when the implicit
// transaction commits. Enforcement itself — the engine refusing to hand over
// another tenant's row — is proven in internal/enginesuite against a real
// server, and every control that depends on it says so and stays partial.
func controlsRls() []control {
	return []control{
		{
			id:     "RLS-01",
			family: "rls",
			title:  "Native RLS is offered by exactly one of the six dialects, and it emits PostgreSQL's set_config",
			want:   partial,
			note: "One engine, not the three the arc plan assumed. The session-variable " +
				"mechanism is hardcoded PostgreSQL (set_config + CREATE POLICY); SQL Server " +
				"would need SESSION_CONTEXT plus CREATE SECURITY POLICY and Oracle DBMS_RLS, " +
				"and neither exists. MySQL/MariaDB have no engine-side RLS at all.",
			probe: probeRlsNativeDialectCoverage,
		},
		{
			id:     "RLS-02",
			family: "rls",
			title:  "On an engine without native RLS, For[T] and Tx refuse — GetClient hands back an unscoped client",
			want:   partial,
			note: "Two of the three public doors of the router fail closed with " +
				"ErrUnsupportedFeature. The third, GetClient — the ClientProvider method, " +
				"exported and documented — returns the base client with no dialect check, " +
				"and reads through it return every tenant's rows. That is the silent " +
				"degradation the strategy claims not to have.",
			probe: probeRlsNativeRefusalOnOtherEngines,
		},
		{
			id:     "RLS-03",
			family: "rls",
			title:  "RowLevelSecurityClient scopes the five builder paths; Find/Update/Delete by primary key emit no tenant predicate, and Create keeps a foreign one",
			want:   partial,
			note: "The injected predicate survives List, Where, Or, UpdateMap and DeleteBy, " +
				"and is dropped by every path that addresses a row by primary key: Find " +
				"overwrites the condition slice, Update(entity) and Delete(entity) build " +
				"their WHERE from the key alone. Create keeps a tenant id the caller " +
				"supplied, so a row can be written under a foreign tenant. This is the only " +
				"row-level option outside PostgreSQL.",
			probe: probeRlsClientPredicatePKPaths,
		},
		{
			id:     "RLS-04",
			family: "rls",
			title:  "VerifyRLSPolicies is reachable and refuses to certify nothing, but nothing calls it at boot",
			want:   partial,
			note: "The preflight reads the PostgreSQL catalog, so a bench without a live " +
				"engine can only measure that it is gated to that dialect and that it " +
				"refuses a client with no registered models; what it reports about a real " +
				"policy is proven in internal/enginesuite. And no code path calls it: a " +
				"Native router serves rows over a database whose policies were never " +
				"installed, without a word.",
			probe: probeRlsVerifyPolicies,
		},
		{
			id:     "RLS-05",
			family: "rls",
			title:  "InstallRLSPolicies renders the isolation DDL for every registered model, with USING and WITH CHECK, and touches nothing on a dry run",
			want:   present,
			probe:  probeRlsInstallPolicyDDL,
		},
		{
			id:     "RLS-06",
			family: "rls",
			title:  "install-rls-policies exists only as an action of the embeddable runner: the `tenant` prefix the docs print is rejected by it",
			want:   absent,
			note: "quarktenant.Run is a runner the application embeds in a main of its own; " +
				"its actions are install-rls-policies and verify-rls-policies, and it " +
				"answers the `tenant` word ADR-0012 and the CHANGELOG print in front of " +
				"them with `unknown action \"tenant\"`. The shipped `quark` binary does have " +
				"a cobra `tenant` group — provision, migrate, list, migrate-all — and no " +
				"install-rls-policies inside it; that binary is a module of its own " +
				"(cmd/quark, ADR-0024) which this bench's module cannot import, so its " +
				"refusal is not what the probe reads. Either way whoever follows the " +
				"documentation types something nothing accepts.",
			probe: probeRlsPolicyCommandLine,
		},
		{
			id:     "RLS-07",
			family: "rls",
			title:  "The router's session variable and the policy's are two independent defaults: when they diverge, neither side complains",
			want:   partial,
			note: "TenantConfig.NativeRLSVar and InstallOptions.NativeRLSVar both default to " +
				"\"app.tenant_id\" and are never compared. Set one and not the other and the " +
				"router writes a variable the policy does not read: current_setting returns " +
				"NULL and the tenant sees zero rows — fail-closed, but mute. The probe " +
				"measures the mute: the router's set_config names the diverging variable, " +
				"the rendered policy names the default, both calls return without error and " +
				"no log record mentions either name. What the bench cannot reach is the one " +
				"place that would name the mismatch, VerifyRLSPolicies — and it compares the " +
				"policy against the INSTALLER's option, not against the router's config, on " +
				"a catalog only a live PostgreSQL has (internal/enginesuite).",
			probe: probeRlsRouterPolicyCoupling,
		},
		{
			id:     "RLS-08",
			family: "rls",
			title:  "Under native RLS the cache key is tenant-scoped: identical statements, one entry per tenant",
			want:   present,
			probe:  probeRlsCacheTenantScoped,
		},
		{
			id:     "RLS-09",
			family: "rls",
			title:  "Under native RLS the builder emits no tenant predicate at all: the policy is the only filter",
			want:   partial,
			note: "The bench can read the delegation — the tenant reaches the session " +
				"variable, the statement carries no WHERE of its own, and storage without " +
				"policies duly returns every tenant's rows — but not the enforcement. That " +
				"a PostgreSQL policy isolates reads and writes for a NOSUPERUSER role is " +
				"proven against a live server in internal/enginesuite " +
				"(TestRowLevelSecurityNativePostgresIsolation).",
			probe: probeRlsNativeDelegatesToEngine,
		},
		{
			id:     "RLS-10",
			family: "rls",
			title:  "The implicit transaction behind a native query commits the write before returning and gives its connection back when the request context ends",
			want:   present,
			probe:  probeRlsNativeImplicitTxLifecycle,
		},
		{
			id:     "RLS-11",
			family: "rls",
			title:  "RawQuery and Exec warn under a Native router; Raw() cannot, and the client-side strategy never warns",
			want:   partial,
			note: "The warning is armed only when the strategy is RowLevelSecurityNative, " +
				"where raw SQL is still filtered by the engine. Under " +
				"RowLevelSecurityClient, where raw SQL really does bypass the only filter " +
				"there is, nothing is logged. And Raw() hands out the pool without a " +
				"context, so it has no tenant to resolve and cannot warn at all.",
			probe: probeRlsRawWarning,
		},
		{
			id:     "RLS-12",
			family: "rls",
			title:  "Sharding and multi-tenancy do not compose: neither a key-routed read nor a scatter-gather honours the tenant in its context",
			want:   absent,
			note: "Both sharded doors return every owner's rows with the tenant sitting in " +
				"the context: the tenancy hook fires on a type assertion to *TenantRouter, " +
				"and a *ShardRouter is a different provider — scatter-gather goes further " +
				"and builds its per-shard query against each shard's raw client. The " +
				"opposite direction is not measured and cannot be: TenantConfig.BaseClient " +
				"is a *Client, so a TenantRouter over a ShardRouter is not something the " +
				"bench can construct. An application with both has to inject the predicate " +
				"by hand in every query.",
			probe: probeRlsShardingWithTenancy,
		},
		{
			id:     "RLS-13",
			family: "rls",
			title:  "Tenant confinement survives a transaction: DatabasePerTenant through its pool, SchemaPerTenant through its schema prefix, RowLevelSecurityClient through its predicate",
			want:   present,
			probe:  probeRlsOtherTenantStrategies,
		},
	}
}
