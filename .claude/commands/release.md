---
description: Cierra una release de Quark complementando lo que release-please automatiza. Sólo cubre los 4 pasos que release-please NO hace (versionado de Docusaurus, MIGRATION_*, sidebar check, deploy verification). Usa este comando tras mergear el PR de release que abre release-please.
argument-hint: vX.Y.Z
---

# /release $ARGUMENTS

Estás cerrando la release **$ARGUMENTS** de Quark. **La mayoría del trabajo
lo hace `release-please`** (ver `release-please-config.json` y
`.github/workflows/release-please.yml`):

- Detecta los Conventional Commits desde el último tag.
- Genera/actualiza `CHANGELOG.md` por sección (`### Added` / `### Fixed` /
  `### Performance` / `### Documentation` / `### Tests`).
- Bumpea la versión en `.release-please-manifest.json`.
- Abre/actualiza un PR llamado `chore(main): release X.Y.Z` que acumula
  cambios hasta que lo mergeas.
- Al mergear el PR, **taggea automáticamente** `vX.Y.Z` y crea la GitHub
  Release.

Este comando cubre los **pasos manuales que release-please NO hace**, en el
orden en que se ejecutan respecto al PR de release.

> **Antes de empezar:** lee `CLAUDE.md` (sección "Regla de release") y
> verifica que **`TASKS.md` no tiene bugs P0 abiertos**. Si los hay, esta
> release no debe taggearse — primero los P0.

## Modo de uso

`$ARGUMENTS` es la versión a publicar (`v0.13.0`, `v1.0.0`, etc.). El
comando se ejecuta **dos veces**:

1. **Antes de aprobar el PR de release** (pasos 1-5): preparación local.
2. **Después de mergear el PR de release** (pasos 6-8): verificación post-merge.

---

## Paso 1 — Validar el PR de release abierto por release-please

```bash
gh pr list --label "autorelease: pending" --json number,title,url
```

Debe existir uno con título `chore(main): release X.Y.Z`. Si no existe:

- Asegúrate de que hay commits Conventional Commits desde el último tag:
  `git log --oneline $(git describe --tags --abbrev=0)..HEAD`
- Revisa que `.github/workflows/release-please.yml` corrió en el último push
  a `main`.

Lee el diff del PR: confirma que el `CHANGELOG.md` generado captura los
cambios reales y que la versión propuesta encaja con la semántica
(major/minor/patch). Si necesitas forzar la versión, usa el commit footer
`Release-As: X.Y.Z` en un commit nuevo y deja que release-please reabra el
PR.

## Paso 2 — Verificar tests cross-engine

```bash
go test -count=1 -short ./...                       # SQLite + unit
go test -count=1 -tags=integration ./...            # 4-5 motores con testcontainers
```

En CI Oracle corre en la matriz `integration` bloqueante (vía
`docker run gvenzl/oracle-free`, no testcontainers). En local, si tu cambio
toca SQL Oracle-specific (MERGE, sequences, etc.) y no tienes el contenedor
arriba, corre con DSN env-var (`QUARK_TEST_ORACLE_DSN`) y déjalo registrado
en el PR.

## Paso 3 — Compilar todos los `examples/`

```bash
for dir in examples/*/; do
  [ -f "$dir/go.mod" ] || [ -f "$dir/main.go" ] || continue
  echo "→ $dir"
  (cd "$dir" && go build -o /dev/null ./...) || exit 1
done
```

Cualquier ejemplo roto = fix antes de seguir. Los ejemplos son la cara
pública del API. Incluye `examples/tenant-rls-native/`, `examples/migrations/`,
y los `examples/{postgres,mysql,mssql,oracle,sqlite}/`.

## Paso 4 — Migration guide si hay breaking changes

`release-please` **no genera `docs/MIGRATION_*.md`** — eso es trabajo manual.
Si el CHANGELOG generado tiene cambios marcados `**BREAKING**` o si algún
commit lleva `BREAKING CHANGE:` en el footer, escribe
`docs/MIGRATION_$ARGUMENTS.md` antes de mergear. Estructura:

- Resumen de qué cambia y por qué.
- Tabla "API antigua → API nueva" con ejemplos.
- Pasos numerados de migración.
- Nota sobre si hay codemod / script de ayuda.

Enlázalo desde el `CHANGELOG.md` (release-please lo respeta si ya está) y
desde el `RELEASE_NOTES_$ARGUMENTS.md` (paso 5).

## Paso 5 — Release notes honestas + Docusaurus version snapshot

### 5.a · `docs/RELEASE_NOTES_$ARGUMENTS.md`

**release-please genera CHANGELOG, no RELEASE_NOTES narrativas.** Si el
proyecto convencionalmente lleva ambos (ver `docs/RELEASE_NOTES_v0.{3..12}.0.md`),
escribe el archivo. Sin lenguaje de marketing — ver `CLAUDE.md` regla
anti-marketing. Estructura:

- Una frase de qué versión es ("Phase X cut: feature/correctness release").
- Sección por categoría con bullets concisos.
- Sección "Known limitations" si aplica (deferrals honestos a la siguiente
  minor / v2.0; sin lenguaje de hype — ver regla anti-marketing).
- Enlace a la página versionada del sitio:
  `https://jcsvwinston.github.io/quark-docs/docs/$ARGUMENTS/`.

### 5.b · Versionar Docusaurus

**release-please NO hace esto.** Si lo saltas, el sitio público pierde la
snapshot de esta versión.

```bash
cd website
npm run docusaurus docs:version $ARGUMENTS
git add docs/ versioned_docs/ versioned_sidebars/ versions.json
```

Esto genera:

- `website/versioned_docs/version-$ARGUMENTS/` (snapshot del contenido actual).
- `website/versioned_sidebars/version-$ARGUMENTS-sidebars.json`.
- Entrada en `website/versions.json`.

### 5.c · Revisar `website/sidebars.ts`

Si la release añadió páginas nuevas en `website/docs/`, deben estar
enlazadas en el sidebar — si no, los usuarios no las encuentran. Diff
manual de:

```bash
# páginas presentes
find website/docs -name '*.mdx' | sed 's|website/docs/||;s|\.mdx$||' | sort -u
# páginas enlazadas en el sidebar
grep -oE "'[a-z][^']*'" website/sidebars.ts | sort -u | tr -d "'"
```

## Paso 6 — Aprobar y mergear el PR de release

Tras los pasos 2-5, comitea los cambios al PR de release (release-please
mantiene el PR vivo y respeta tus commits adicionales sobre la rama
`release-please--branches--main`):

```bash
git checkout release-please--branches--main
git pull
git add docs/MIGRATION_$ARGUMENTS.md docs/RELEASE_NOTES_$ARGUMENTS.md website/
git commit -m "docs: $ARGUMENTS DoD — migration guide + docs versioning"
git push
```

Aprueba el PR y mergéalo (squash o merge según convención). En cuanto
toque `main`, el workflow `release-please` taggea `vX.Y.Z` automáticamente
y crea la GitHub Release.

## Paso 7 — Verificar deploy del sitio

El tag dispara `.github/workflows/deploy.yml` (o `deploy-docs.yml` según
nombre actual) que:

1. `cd website && npm ci && npm run build`.
2. Pushea `website/build/` a `jcsvwinston/quark-docs` rama `gh-pages`.

```bash
# Confirmar que el job corrió y terminó OK
gh run list --workflow=deploy.yml --limit 3

# Confirmar que la versión está en el sitio
curl -sI https://jcsvwinston.github.io/quark-docs/docs/$ARGUMENTS/ | head -1
```

Si falla, arranca manualmente `cd website && npm run build && npm run deploy`
con el PAT correcto e investiga la action.

## Paso 8 — Post-release

```bash
git checkout main && git pull
```

- Actualiza `TASKS.md`: tachar los items que la release cierra (`~~F-N ...~~`
  + bloque "**Cerrado**").
- Si la release **cierra una Fase**, actualiza el bloque-cabecera de
  `TASKS.md` con la nueva fase abierta y abre el ADR de apertura si toca.
- Corre **`docs-auditor`** una vez tras la release para confirmar que no
  quedaron desalineamientos: `/doc-sync` (en modo report) y revisa el
  informe.

---

## Si algo falla a media release

- **Tests rojos**: rollback de los commits del paso 5/6 en la rama de
  release-please. Fix, vuelve a empezar el paso 2.
- **`docusaurus docs:version` con conflicto**: si una versión previa
  estaba mal, edita `versions.json` con cuidado. **No borres `versioned_docs/`
  salvo que sepas exactamente lo que haces.**
- **release-please no abre PR**: confirma que `.github/workflows/release-please.yml`
  está en `main` y que los commits desde el último tag son Conventional
  Commits con tipos válidos (`feat`, `fix`, `perf`, `refactor`, `revert`,
  `docs`, `test`).
- **Tag duplicado**: si taggeaste manualmente Y release-please también
  taggeó, borra el tag manual (`git tag -d vX.Y.Z && git push --delete origin vX.Y.Z`)
  — el de release-please es la fuente de verdad.

---

**Recordatorio final:** este comando existe porque la primera auditoría de
Quark detectó incoherencia entre `RELEASE_NOTES_V1` (v1.0 production-ready)
y `CHANGELOG.md` (en 0.1.1). **release-please cubre lo que era el 80% del
checklist viejo**; este comando se queda con el 20% que la automatización
no toca (Docusaurus snapshot, MIGRATION guide narrativa, RELEASE_NOTES
narrativas, deploy verification). No saltes ninguno de esos cuatro — son
las cosas que rompen la coherencia pública entre release y release.

**Nota histórica:** versiones anteriores de este comando documentaban un
flujo manual (`gh pr create`, `git tag -a`, bump de versión a mano) que
ahora son redundantes con release-please y, si se ejecutan, generan PRs y
tags duplicados. La regla nueva: **release-please es la fuente de verdad
del versionado y el changelog**; este comando complementa, no compite.
