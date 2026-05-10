---
description: Cierra una release de Quark con su Definition of Done completo (versión, CHANGELOG, docs Docusaurus versionadas, ejemplos, breaking changes, tag).
argument-hint: vX.Y.Z
---

# /release $ARGUMENTS

Estás cerrando la release **$ARGUMENTS** de Quark. **No taggees ni publiques nada hasta que cada paso de este checklist esté verificado**. Si alguno falla, abre los issues correspondientes y para. La release se ejecuta en un único PR llamado `release: $ARGUMENTS`.

## Antes de empezar

Lee `CLAUDE.md` (sección "Regla de release") y `docs/ANALISIS_MADUREZ.md` §0 si no lo has hecho en esta sesión. Asume estado actual = `main` limpio.

Verifica que **`TASKS.md` no tiene bugs P0 abiertos**. Si los hay, esta release no debe taggearse — primero los P0.

## Pasos del Definition of Done

### 1. Validar tests en los 6 motores

```bash
go test -count=1 -short ./...                                # rápido (SQLite + unit)
go test -count=1 -tags=integration ./...                     # los 6 motores con testcontainers
```

Si la integración aún no está montada (TASKS F0-8 pendiente), corre manualmente con DSN env vars y déjalo registrado en el PR. **No taggees sin los 6 verdes.**

### 2. Compilar todos los `examples/`

```bash
for dir in examples/*/; do
  echo "→ $dir"
  (cd "$dir" && go build -o /dev/null ./...) || exit 1
done
```

Cualquier ejemplo roto = fix antes de seguir. Los ejemplos son la cara pública del API.

### 3. Generar / actualizar CHANGELOG.md

Si `release-please` está configurado (TASKS F0-9), revisa el PR que ya generó. Si no:

```bash
git log --pretty=format:"%s (%h)" v$(cat website/versions.json | jq -r '.[0]')..HEAD
```

Clasifica por sección Keep a Changelog: `### Added`, `### Changed`, `### Deprecated`, `### Removed`, `### Fixed`, `### Security`. **Toda entrada debe enlazar al PR/commit.** Si hay `BREAKING CHANGE:` en commits, debe aparecer en `### Changed` con marca clara `**BREAKING**` y enlace a `MIGRATION_$ARGUMENTS.md`.

### 4. Bumpear versión en lugares que la mencionan

```bash
# Buscar dónde aparece la versión vieja
grep -rn "v0\.[0-9]\+\.[0-9]\+" --include="*.md" --include="*.go" --include="*.json" --include="*.ts" .
```

Actualiza:

- `README.md` — badges, snippet de `go get github.com/jcsvwinston/quark@$ARGUMENTS`, ejemplos.
- `website/docusaurus.config.ts` — si hay versión hardcoded en navbar.
- `website/package.json` — si tu workflow lo trackea.
- `SECURITY.md` — la versión soportada actual.
- `docs/ROADMAP.md` — mover items de "in progress" a la sección de la versión.

### 5. Versionar Docusaurus

```bash
cd website
npm run docusaurus docs:version $ARGUMENTS
```

Esto genera:

- `website/versioned_docs/version-$ARGUMENTS/`  (snapshot del contenido actual de `website/docs/`)
- `website/versioned_sidebars/version-$ARGUMENTS-sidebars.json`
- entrada en `website/versions.json`

```bash
git add website/versioned_docs/ website/versioned_sidebars/ website/versions.json
```

### 6. Revisar `website/sidebars.ts`

Asegúrate de que toda página nueva en `website/docs/` está enlazada en el sidebar. Si añadiste una sección (ej. JSON queries) y no aparece en navegación, los usuarios no la encuentran.

```bash
# lista páginas presentes vs enlazadas
ls website/docs/**/*.md | sed 's|website/docs/||;s|\.md$||'
grep -oE '"[^"]+"' website/sidebars.ts | sort -u
```

Diff manual de las dos listas.

### 7. Migration guide si hay breaking changes

Si los commits incluyen `BREAKING CHANGE:`, escribe `docs/MIGRATION_$ARGUMENTS.md`. Estructura:

- Resumen de qué cambia y por qué.
- Tabla "API antigua → API nueva" con ejemplos.
- Pasos numerados de migración.
- Nota sobre si hay codemod / script de ayuda (idealmente sí).

Enlázalo desde `website/docs/migrations/` y desde el `RELEASE_NOTES_$ARGUMENTS.md`.

### 8. Release notes honestas

`docs/RELEASE_NOTES_$ARGUMENTS.md`. **Sin lenguaje de marketing.** Ver `CLAUDE.md` regla anti-marketing. Estructura:

- Una frase de qué versión es (ej. "Patch release: 5 bugfixes y limpieza de versionado.").
- Sección por categoría con bullets concisos.
- Sección "Known limitations" si aplica (alpha-late, recordar al lector que no es production-ready hasta v1.0).
- Enlace a la página versionada del sitio: `https://jcsvwinston.github.io/quark-docs/docs/$ARGUMENTS/`.

### 9. Validación final estática

```bash
# Linter de docs (TASKS F0-10)
make lint-docs    # cuando esté disponible; mientras: revisa a ojo

# Coherencia versionado
grep -rn "RELEASE_NOTES_V1" .   # debe estar vacío salvo en historic git
grep -rn "production-ready\|enterprise-grade" --include="*.md" .   # ojo a marketing
```

### 10. Commit y PR

```bash
git checkout -b release/$ARGUMENTS
git add -A
git commit -m "chore(release): $ARGUMENTS"
git push -u origin release/$ARGUMENTS
gh pr create \
  --title "release: $ARGUMENTS" \
  --body-file docs/RELEASE_NOTES_$ARGUMENTS.md \
  --label release
```

PR pasa por:

- CI verde (los 6 motores).
- Aprobación del subagente `code-reviewer`.
- Review humana opcional pero recomendada.

### 11. Tag y deploy de docs

Tras mergear:

```bash
git checkout main && git pull
git tag -a $ARGUMENTS -m "Release $ARGUMENTS"
git push origin $ARGUMENTS
```

Esto dispara `.github/workflows/deploy-docs.yml` que:

1. `cd website && npm ci && npm run build`
2. Pushea `website/build/` a `jcsvwinston/quark-docs` rama `gh-pages`.

Verifica en GitHub Actions que el deploy terminó OK. Visita `https://jcsvwinston.github.io/quark-docs/docs/$ARGUMENTS/` y confirma que sirve la versión nueva.

### 12. GitHub Release

```bash
gh release create $ARGUMENTS \
  --title "Quark $ARGUMENTS" \
  --notes-file docs/RELEASE_NOTES_$ARGUMENTS.md
```

Adjunta artefactos si los hay (binarios del CLI, etc.).

## Si algo falla a media release

- **Tests rojos**: rollback del PR, fix, vuelve a empezar el checklist desde el paso fallido.
- **Deploy de docs falla**: arranca manualmente `cd website && npm run build && npm run deploy` con el PAT correcto. Investiga la action.
- **Versionado de Docusaurus generó conflicto**: si una versión anterior estaba mal, edita `versions.json` con cuidado. **No borres `versioned_docs/` salvo que sepas exactamente lo que haces.**

## Después de la release

- Actualiza `TASKS.md` cerrando los items que la release resuelve.
- Si abriste fase nueva (ej. release de v0.3 cierra Fase 1), abre los issues de la siguiente fase con label `phase-N+1`.
- Comunica la release donde corresponda (Slack interno de Nucleus, blog, etc.).

---

**Recordatorio final:** este comando existe porque la primera auditoría de Quark detectó incoherencia entre `RELEASE_NOTES_V1` (v1.0 production-ready) y `CHANGELOG.md` (en 0.1.1). No repitamos eso. Si saltas un paso "porque es rápido", probablemente acabas de crear la próxima discrepancia.
