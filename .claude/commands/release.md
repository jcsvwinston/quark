---
description: Cierra una release de Quark complementando lo que release-please automatiza. Cubre los pasos manuales que release-please NO hace (bump del version-statement en 5 docs, secciones de release-notes, versionado de Docusaurus sólo en minors, MIGRATION_* sólo en breaking, sidebar check, deploy verification); el flujo distingue patch de minor. Usa este comando tras mergear el PR de release que abre release-please.
argument-hint: vX.Y.Z
---

# /release $ARGUMENTS

Estás cerrando la release **$ARGUMENTS** de Quark. **La mayoría del trabajo
lo hace `release-please`** (ver `release-please-config.json` y
`.github/workflows/release-please.yml`):

- Detecta los Conventional Commits desde el último tag.
- Genera/actualiza `CHANGELOG.md` por sección (`### Added` / `### Fixed` /
  `### Performance` / `### Changed` / `### Reverted`) a partir de
  feat/fix/perf/refactor/revert. **`docs`/`test`/`chore` están `hidden`** en
  `release-please-config.json`: no aparecen en el CHANGELOG **ni disparan un
  release** — un cambio docs-only no corta versión (evita el release circular
  por el bump de docs del paso 5).
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

## Paso 5 — Docs al día con la versión

Es la parte que más se salta y la que rompe la coherencia pública (v1.1.3 se
publicó sin ella). **Distingue patch de minor** — no todo aplica a un patch.

### 5.a · Bump del "version statement" — TODA release (patch y minor)

La frase **"Quark is `vX.Y.Z` on the stable `v1.x` line"** vive en **cinco**
sitios; bumpéalos todos a la versión nueva (saltarse esto fue exactamente el
fallo de v1.1.3):

- `README.md`
- `website/docs/intro.mdx`
- `website/docs/guides/installation.mdx`
- `website/docs/reference/release-notes.mdx` (la frase **y** una sección `## vX.Y.Z`, ver 5.b)
- `website/docs/reference/roadmap.mdx`

Caza los que falten antes de cerrar:

```bash
grep -rnE "Quark is( at)? \*\*v?[0-9]+\.[0-9]+\.[0-9]+" README.md website/docs/ | grep -v versioned_docs
```

### 5.b · Notas de release

- **`website/docs/reference/release-notes.mdx`** lleva una **sección por
  versión** (`## vX.Y.Z`, newest-first). Añádela en **toda** release (patch
  incluido): bullets concisos de los fixes/features, sin marketing
  (`CLAUDE.md` regla anti-marketing). Cierra con "No breaking changes." si
  aplica.
- **`docs/RELEASE_NOTES_vX.Y.0.md`** (archivo narrativo en `docs/`) es **sólo
  para minors** — ver `docs/RELEASE_NOTES_v1.1.0.md`. Los patches **no** lo
  llevan (mira `docs/RELEASE_NOTES_*.md`: sólo hay minors).

### 5.c · Snapshot de Docusaurus — SÓLO en minors (X.Y.0)

El repo versiona la doc **por minor**: `versioned_docs/` salta `1.0.0 →
1.1.0` y `versions.json` lista sólo minors. **Los patches NO se snapshotean**
— la snapshot de la minor + la doc "next" (`website/docs/`) cubren toda la
línea `vX.Y.x`. En un **minor**:

```bash
cd website
npm run docusaurus docs:version X.Y.Z   # SIN prefijo 'v' → genera version-X.Y.Z
git add docs/ versioned_docs/ versioned_sidebars/ versions.json
```

(Pasar `vX.Y.Z` con la `v` generaría `version-vX.Y.Z`, fuera de la convención
`version-1.1.0`.) Genera `versioned_docs/version-X.Y.Z/`, su
`versioned_sidebars/`, y una entrada en `versions.json`.

### 5.d · Revisar `website/sidebars.ts`

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

> **Alternativa más robusta (la usada en v1.1.4):** release-please
> **force-pushea** su rama cuando regenera el PR, así que commits que le
> añadas pueden perderse si entra otro commit a `main` antes del merge. Es
> más seguro **mergear el PR de release tal cual** (sólo CHANGELOG +
> manifest) y hacer el bump de docs del paso 5 en un **PR `docs:` aparte,
> post-merge**. Coste: una ventana corta en la que el tag existe pero los
> version-statements aún dicen la versión anterior — la cierras enseguida con
> el PR de docs. Para un **patch** (sin snapshot ni MIGRATION) este PR de
> docs es sólo el bump de los 5 version-statements + la sección de
> release-notes.

## Paso 7 — Verificar deploy del sitio

`.github/workflows/deploy.yml` ("Deploy Docusaurus to GitHub Pages") corre
**en cada push a `main` que toque `website/`** (está path-filtered): hace
`npm ci && npm run build` y publica `website/build/` a las **GitHub Pages del
repo `quark`** vía `actions/deploy-pages` — https://jcsvwinston.github.io/quark/.
**No** usa la rama `gh-pages` ni el repo `quark-docs` (el sitio se movió a este
repo en v0.3.0).

OJO: el merge del PR de release-please que sólo toca `CHANGELOG.md` +
`.release-please-manifest.json` **no dispara deploy** (no toca `website/`), y
no hace falta. Es tu PR de doc-bump del paso 5 (que sí toca `website/`) el que
redeploya el sitio con la versión nueva.

```bash
# Confirmar que el deploy del último push a website/ terminó OK
gh run list --workflow=deploy.yml --limit 3

# El sitio responde (la versión por defecto es la última snapshot minor,
# no el patch — los patches no se snapshotean)
curl -sI https://jcsvwinston.github.io/quark/ | head -1
```

Si falla, revisa la action `deploy.yml`: el build de Docusaurus corre con
`onBrokenLinks:'throw'`, así que un link interno roto la tumba.

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
Quark detectó incoherencia entre `RELEASE_NOTES_V1` (que vendía v1.0 como
lista para producción) y `CHANGELOG.md` (en 0.1.1). **release-please cubre lo que era el 80% del
checklist viejo**; este comando se queda con lo que la automatización no
toca (bump del version-statement en los 5 docs, secciones/notas de release,
Docusaurus snapshot en minors, MIGRATION en breaking, deploy verification).
**No saltes el bump del version-statement** — saltárselo es lo que dejó
v1.1.3 publicada con los docs 2 patches atrás (caught up en v1.1.4).

**Nota histórica:** versiones anteriores de este comando documentaban un
flujo manual (`gh pr create`, `git tag -a`, bump de versión a mano) que
ahora son redundantes con release-please y, si se ejecutan, generan PRs y
tags duplicados. La regla nueva: **release-please es la fuente de verdad
del versionado y el changelog**; este comando complementa, no compite.
