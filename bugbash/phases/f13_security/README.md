# F13 — Negative tests (security)

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F13.

## Qué prueba

Que `internal/guard.SQLGuard` bloquea lo que debe bloquear, en los 6 motores.
Es la red anti-inyección: cada payload malicioso conocido debe ser **rechazado
con un error antes de ejecutarse**. Un payload que llega al motor es **P0**
(va al top de `TASKS.md`, no a la sección de hallazgos).

## Vectores cubiertos

- **Identifier:** `Where(payload, "=", x)` con identifiers maliciosos
  (`id; DROP TABLE orders--`, `1=1`, `id) OR (1=1`, comentarios) →
  `ValidateIdentifier`. Tras los payloads de DROP/DELETE, un `Count()` limpio
  confirma que la tabla sigue intacta (cero fuga al motor).
- **Operator:** `Where("status", payload, x)` con operadores no whitelisted
  (`; DROP`, `OR`, `UNION`) → `ValidateOperator`.
- **JSONPath:** `WhereJSON("settings", payload, …)` con paths maliciosos →
  `ValidateJSONPath`; además se asserta `errors.Is(err, ErrInvalidJSONPath)`.
- **JoinOn:** `Join(t).OnRaw(payload)` y `.On(left,op,right)` con inyección →
  `ValidateJoinOn`; se asserta `errors.Is(err, ErrInvalidJoin)`.
- **RawQueryDisabled:** `RawQuery`/`Exec` con `AllowRawQueries=false` (default)
  → rechazado; se asserta `errors.Is(err, ErrInvalidQuery)`.
- **RawQueryInjection:** con `AllowRawQueries=true`, `ValidateRawQuery` sigue
  bloqueando consultas sin placeholders y con patrones de inyección
  (`UNION SELECT`, `OR 1=1`, `;DROP`, `;DELETE`, comentario de línea `--`).
  También cubre `Exec` (DML) con stacked-statement.

  **Limitación documentada (no es vector cubierto):** los comentarios de bloque
  `/* */` se permiten a propósito — son *optimizer hints* legítimos en raw
  queries (`/*+ INDEX(...) */`). Por tanto la evasión por bloque
  (`UNION/**/SELECT`) **no** la atrapa `ValidateRawQuery`. Es un backstop
  heurístico, no un filtro anti-inyección completo: la frontera real de las raw
  queries es `AllowRawQueries` (off por defecto) + placeholders para valores.
  Ver `docs/playbooks/security.md`.
- **TenantID:** `TenantRouter.ResolveTenant` rechaza tenant IDs fuera de
  `^[a-z0-9_-]+$` (`'; DROP--`, `a.b`, `acme OR 1=1`) y acepta uno bien formado
  (sin falsos positivos).

## Nota sobre el tipado del error

El contrato primario de F13 es **bloqueado** (la consulta no se ejecuta). Donde
Quark envuelve un sentinel público (`ErrInvalidJSONPath`, `ErrInvalidJoin`,
`ErrInvalidQuery`) se asserta también `errors.Is`. Los caminos de
identifier/operator devuelven hoy el error descriptivo del guard (string
`ErrInvalidIdentifier:` / `ErrInvalidQuery:`) en vez de envolver el sentinel
público; F13 verifica que **bloquean** (la garantía de seguridad) sin
sobre-assertar un sentinel que el builder no envuelve. Unificar ese wrapping es
una mejora menor de consistencia de API, separada de esta fase.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestNegativeSecurity -v ./phases/f13_security/                  # SQLite
go test -tags=bugbash -run TestNegativeSecurity -v ./phases/f13_security/ -engines=all -timeout 20m
```

## Criterio done

- [x] 100% de los payloads conocidos bloqueados en los 6 motores.
- [x] Sentinels públicos verificados donde el builder los envuelve.

Cualquier payload que pase (error nil) → `reporter.Fail` con severidad **P0**.
