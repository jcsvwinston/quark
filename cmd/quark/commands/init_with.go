// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/fatih/color"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	clidb "github.com/jcsvwinston/quark/cmd/quark/internal/db"
)

// integrationFixtures is the code `quark init --with` writes: byte-for-byte
// copies of the tested fixtures in internal/integrations — the module CI
// compiles against each framework and drives over the network — one file
// per fixture file, with ".tmpl" appended so the go command does not build
// them here. They are not text/templates. init rewrites each in a fixed way
// (rewriteFixture: the package clause and its doc comment, the import paths,
// the driver module of the dialect) and the code in between stays the
// fixture's, so a scaffolded project starts from what the frameworks guide
// shows and what CI tests. TestInitWithTemplatesAreTheFixtures fails when a
// copy and its fixture differ; `make regen` refreshes the copies.
//
//go:embed templates/integrations
var integrationFixtures embed.FS

// fixtureRoot is where the copies live inside integrationFixtures, and
// fixtureModule the import path of the module they were copied from.
const (
	fixtureRoot   = "templates/integrations"
	fixtureModule = "github.com/jcsvwinston/quark/internal/integrations"
)

// cliModule is this CLI's module, which a scaffolded go.mod requires for the
// runner in cmd/<app>.
const cliModule = "github.com/jcsvwinston/quark/cmd/quark"

// fixtureDriver is the driver module every fixture blank-imports; init puts
// the dialect's in its place.
const fixtureDriver = libraryModule + "/drivers/sqlite"

// serverKind says what cmd/<app>-server/main.go init writes beside an
// integration: an http.Server around the handler, a gRPC server, or nothing
// (a Nucleus module is mounted by the host application's main).
type serverKind int

const (
	serveNothing serverKind = iota
	serveHTTP
	serveGRPC
)

// integration is one target of `quark init --with`.
type integration struct {
	target    string // the --with value
	framework string // as the frameworks guide's heading spells it
	fixture   string // the fixture package, a directory of internal/integrations
	// rename maps a fixture file to the name it takes in the project; a
	// file not listed keeps its name.
	rename map[string]string
	// requires are the framework modules the written code imports, at the
	// version go.mod is given. Each is the fixture module's own requirement
	// (or older: TestInitWithPinsAreTheFixtures) — the version CI tests.
	requires    []module.Version
	server      serverKind
	constructor string // what the server main calls on the package (serveHTTP, serveGRPC)
	addr        string // the server main's default listen address
}

// integrations lists what --with writes, in the order initWithTargets
// names them (TestInitWithTargetsAreTheIntegrations holds the two together).
var integrations = []integration{
	{
		target: "chi", framework: "chi", fixture: "withchi",
		requires:    []module.Version{{Path: "github.com/go-chi/chi/v5", Version: "v5.3.2"}},
		server:      serveHTTP,
		constructor: "NewRouter",
		addr:        ":8080",
	},
	{
		target: "echo", framework: "Echo", fixture: "withecho",
		requires:    []module.Version{{Path: "github.com/labstack/echo/v5", Version: "v5.4.0"}},
		server:      serveHTTP,
		constructor: "NewServer",
		addr:        ":8080",
	},
	{
		target: "gin", framework: "Gin", fixture: "withgin",
		requires:    []module.Version{{Path: "github.com/gin-gonic/gin", Version: "v1.12.0"}},
		server:      serveHTTP,
		constructor: "NewEngine",
		addr:        ":8080",
	},
	{
		target: "grpc", framework: "gRPC", fixture: "withgrpc",
		requires: []module.Version{
			{Path: "google.golang.org/grpc", Version: "v1.84.0"},
			{Path: "google.golang.org/protobuf", Version: "v1.36.12"},
		},
		server:      serveGRPC,
		constructor: "NewServer",
		addr:        ":50051",
	},
	{
		target: "nucleus", framework: "Nucleus", fixture: "withnucleus",
		rename:   map[string]string{"nucleus.go": "module.go"},
		requires: []module.Version{{Path: "github.com/jcsvwinston/nucleus", Version: "v1.30.1"}},
	},
}

func integrationFor(target string) *integration {
	for i := range integrations {
		if integrations[i].target == target {
			return &integrations[i]
		}
	}
	return nil
}

// scaffold is what one `quark init --with` run writes into a project.
type scaffold struct {
	in      *integration
	module  string // the project's import path for the init directory
	project string // the project name (cmd/<project>)
	pkg     string // the Go package of internal/<pkg>
	dialect string // as --dialect spelled it
}

func (s scaffold) data() map[string]string {
	return map[string]string{
		"Target":      s.in.target,
		"Framework":   s.in.framework,
		"Module":      s.module,
		"Project":     s.project,
		"Package":     s.pkg,
		"Driver":      clidb.DriverName(s.dialect),
		"DSN":         getDSNPlaceholder(s.dialect),
		"Constructor": s.in.constructor,
		"Addr":        s.in.addr,
	}
}

// file is one file of the scaffold: where it goes, relative to the init
// directory, and its content.
type file struct {
	path    string
	content []byte
}

// files renders the integration: the shared notes package, the fixture
// package of the framework, and the server main when there is one.
func (s scaffold) files() ([]file, error) {
	var out []file
	for _, dir := range []string{"notes", s.in.fixture} {
		err := fs.WalkDir(integrationFixtures, path.Join(fixtureRoot, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			src, err := integrationFixtures.ReadFile(p)
			if err != nil {
				return err
			}
			rel := strings.TrimSuffix(strings.TrimPrefix(p, fixtureRoot+"/"), ".tmpl")
			content, err := s.rewriteFixture(rel, src)
			if err != nil {
				return fmt.Errorf("rewriting %s for --with %s: %w", rel, s.in.target, err)
			}
			out = append(out, file{path: s.destination(rel), content: content})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if s.in.server != serveNothing {
		src := withServerHTTPTemplate
		if s.in.server == serveGRPC {
			src = withServerGRPCTemplate
		}
		content, err := renderGo("server main", src, s.data())
		if err != nil {
			return nil, err
		}
		out = append(out, file{path: path.Join("cmd", s.project+"-server", "main.go"), content: content})
	}
	return out, nil
}

// destination maps a fixture file (relative to internal/integrations) to
// its place in the project: notes/ to internal/notes/, the framework's
// package to internal/<pkg>/.
func (s scaffold) destination(rel string) string {
	if strings.HasPrefix(rel, "notes/") {
		return path.Join("internal", rel)
	}
	rest := strings.TrimPrefix(rel, s.in.fixture+"/")
	if to, ok := s.in.rename[rest]; ok {
		rest = to
	}
	return path.Join("internal", s.pkg, rest)
}

// importRewrites are the textual substitutions that move a fixture file into
// the project: the import paths of the fixtures' module become the
// project's, and the SQLite driver module the dialect's. Nothing else in the
// code changes.
func (s scaffold) importRewrites() *strings.Replacer {
	return strings.NewReplacer(
		`"`+fixtureModule+`/notes"`, `"`+s.module+`/internal/notes"`,
		fixtureModule+"/"+s.in.fixture+"/", s.module+"/internal/"+s.pkg+"/",
		"internal/integrations/"+s.in.fixture+"/", "internal/"+s.pkg+"/",
		`"`+fixtureDriver+`"`, `"`+libraryModule+"/"+driverModuleFor(s.dialect)+`"`,
	)
}

// licenseHeader is the header every file of this repository starts with; a
// scaffolded file is the user's, so it is dropped.
var licenseHeader = regexp.MustCompile(`\A// Copyright [^\n]*\n// SPDX-License-Identifier: [^\n]*\n\n`)

// packageClause finds the package clause; what is above it, once the
// license header is gone, is the package's doc comment.
var packageClause = regexp.MustCompile(`(?m)^package (\w+)\n`)

// rewriteFixture turns one fixture file into the project's.
//
// For a hand-written Go file the doc comment and the package clause are the
// scaffold's (the fixture's describe a fixture) and the rest is the
// fixture's, with the import paths rewritten. A generated file keeps its
// header and package; the descriptor protoc-gen-go embeds names the Go
// package it was generated for, so it is re-encoded with the project's
// (rewriteRawDescGoPackage) — the bytes buf would write for the project.
func (s scaffold) rewriteFixture(rel string, src []byte) ([]byte, error) {
	src = licenseHeader.ReplaceAll(src, nil)
	switch {
	case strings.HasSuffix(rel, ".pb.go"):
		if rawDescConst.Match(src) {
			var err error
			src, err = rewriteRawDescGoPackage(src, s.module+"/"+path.Dir(s.destination(rel)))
			if err != nil {
				return nil, err
			}
		}
	case strings.HasSuffix(rel, ".go"):
		loc := packageClause.FindSubmatchIndex(src)
		if loc == nil {
			return nil, fmt.Errorf("no package clause")
		}
		docSrc, ok := scaffoldDocs[rel]
		if !ok {
			return nil, fmt.Errorf("no doc comment for it in scaffoldDocs")
		}
		doc, err := renderText("doc comment of "+rel, docSrc, s.data())
		if err != nil {
			return nil, err
		}
		name := string(src[loc[2]:loc[3]])
		if rel != "notes/notes.go" {
			name = s.pkg
		}
		src = append([]byte(wrapDoc(doc, docWidth)+"package "+name+"\n"), src[loc[1]:]...)
	}
	src = []byte(s.importRewrites().Replace(string(src)))
	if bytes.Contains(src, []byte("internal/integrations")) {
		return nil, fmt.Errorf("a path of the fixtures' module is left after the rewrite")
	}
	if strings.HasSuffix(rel, ".go") {
		formatted, err := format.Source(src)
		if err != nil {
			return nil, fmt.Errorf("the rewritten file does not parse: %w", err)
		}
		src = formatted
	}
	return src, nil
}

// scaffoldDocs are the doc comments of the hand-written files, keyed by
// fixture path. Each says what the package is in the project and where its
// code comes from; the fixture's own doc comment describes a fixture.
var scaffoldDocs = map[string]string{
	"notes/notes.go": `// Package notes is the model of the notes API internal/{{.Package}} serves,
// scaffolded by 'quark init --with {{.Target}}': the Note table, the Draft a
// client sends, and Status, the HTTP status for an error a handler meets —
// decided by Quark's error classification, never by a driver's message.
// Replace Note with your own model and keep the shape.
`,
	"withchi/chi.go": `// Package {{.Package}} serves the notes API with go-chi/chi, scaffolded by
// 'quark init --with chi'. It is the code of the chi section of Quark's
// frameworks guide, which CI compiles and tests: one *quark.Client shared by
// every handler, the request's context on every query, one transaction for
// a request that writes more than once, and Quark's error classification
// turned into the status. cmd/{{.Project}}-server serves it.
`,
	"withecho/echo.go": `// Package {{.Package}} serves the notes API with labstack/echo v5, scaffolded
// by 'quark init --with echo'. It is the code of the Echo section of Quark's
// frameworks guide, which CI compiles and tests: one *quark.Client shared by
// every handler, the request's context on every query, one transaction for
// a request that writes more than once, and Quark's errors mapped to a
// status in one place, the server's HTTPErrorHandler.
// cmd/{{.Project}}-server serves it.
`,
	"withgin/gin.go": `// Package {{.Package}} serves the notes API with gin-gonic/gin, scaffolded by
// 'quark init --with gin'. It is the code of the Gin section of Quark's
// frameworks guide, which CI compiles and tests: one *quark.Client shared by
// every handler, the request's context on every query, one transaction for
// a request that writes more than once, and one middleware that turns the
// error a handler recorded into the status. cmd/{{.Project}}-server serves it.
`,
	"withgrpc/grpc.go": `// Package {{.Package}} serves the notes API as a gRPC service (notespb,
// generated from notespb/notes.proto), scaffolded by 'quark init --with grpc'.
// It is the code of the gRPC section of Quark's frameworks guide, which CI
// compiles and tests: every method queries with the call's context, an
// import writes in one transaction, and one unary interceptor gives Quark's
// errors their status code. cmd/{{.Project}}-server serves it. After editing
// the .proto, regenerate notespb with 'buf generate' in internal/{{.Package}}/notespb.
`,
	"withnucleus/nucleus.go": `// Package {{.Package}} is the notes feature as a Nucleus module wrapping a
// *quark.Client, scaffolded by 'quark init --with nucleus'. It is the code of
// the Nucleus section of Quark's frameworks guide, which CI compiles and
// tests. Its handlers query with the request's context and return their
// errors, and fail gives a Quark error the status notes.Status decides. main
// builds the client and the Nucleus application, then mounts this module;
// the module owns its schema (migrated in OnStart), its routes and its
// access rules:
//
//	client, err := quark.New("{{.Driver}}", dsn)
//	app, err := nucleus.New().
//		FromConfigFile("nucleus.yml").
//		Mount({{.Package}}.Module(client)).
//		Build()
//
// For a whole application generated around this seam — config, policy file,
// migrations, admin panel — start from the Nucleus side instead:
//
//	nucleus new {{.Project}} --with quark
`,
}

func renderText(name, src string, data any) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(src)
	if err != nil {
		return "", fmt.Errorf("parsing the %s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering the %s: %w", name, err)
	}
	return buf.String(), nil
}

func renderGo(name, src string, data any) ([]byte, error) {
	text, err := renderText(name, src, data)
	if err != nil {
		return nil, err
	}
	if i := strings.Index(text, "\npackage "); i >= 0 {
		text = wrapDoc(text[:i+1], docWidth) + text[i+1:]
	}
	out, err := format.Source([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("the %s does not parse: %w", name, err)
	}
	return out, nil
}

// docWidth is the column a scaffold's doc comments are wrapped at.
const docWidth = 79

// wrapDoc re-flows the prose paragraphs of a // comment block to width
// columns, so that a long project or package name does not leave the doc
// comment ragged. Code lines (//<tab>) and empty ones (//) stay as they are,
// and a 'quoted command' is never split across two lines.
func wrapDoc(doc string, width int) string {
	var out, words []string
	flush := func() {
		line := "//"
		for i := 0; i < len(words); i++ {
			w := words[i]
			for strings.HasPrefix(w, "'") && !closesQuote(w) && i+1 < len(words) {
				i++
				w += " " + words[i]
			}
			if line != "//" && len(line)+1+len(w) > width {
				out = append(out, line)
				line = "//"
			}
			line += " " + w
		}
		if line != "//" {
			out = append(out, line)
		}
		words = nil
	}
	for _, l := range strings.Split(strings.TrimSuffix(doc, "\n"), "\n") {
		if strings.HasPrefix(l, "// ") {
			words = append(words, strings.Fields(l[3:])...)
			continue
		}
		flush()
		out = append(out, l)
	}
	flush()
	return strings.Join(out, "\n") + "\n"
}

// closesQuote reports whether a 'quoted span that starts at w also ends in
// it, before any trailing punctuation.
func closesQuote(w string) bool {
	w = strings.TrimRight(w, ".,;:)")
	return len(w) > 1 && strings.HasSuffix(w, "'")
}

// writeIntegration writes the files of an integration under dir. An
// existing file is never overwritten.
func writeIntegration(dir string, files []file) error {
	for _, f := range files {
		dst := filepath.Join(dir, filepath.FromSlash(f.path))
		if _, err := os.Stat(dst); err == nil {
			color.Yellow("Warning: %s already exists. Skipping.", f.path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, f.content, 0o644); err != nil {
			return fmt.Errorf("creating %s: %w", f.path, err)
		}
		fmt.Printf("  Created %s\n", f.path)
	}
	return nil
}

// --- go.mod ------------------------------------------------------------------

// suiteRequirements are the Quark modules a scaffolded go.mod requires: the
// library this binary was built against and this CLI's own module (the
// runner in cmd/<app> embeds its command tree), at the versions this binary
// carries. A development build carries none worth writing — "devel", or a
// pseudo-version of a commit no proxy may have — and leaves both to
// `go mod tidy`, which resolves them to their latest release.
func suiteRequirements() (pinned []module.Version, unpinned []string) {
	for _, m := range []module.Version{
		{Path: libraryModule, Version: quarkLibraryVersion()},
		{Path: cliModule, Version: cliVersion()},
	} {
		if releaseVersion(m) {
			pinned = append(pinned, m)
		} else {
			unpinned = append(unpinned, m.Path)
		}
	}
	return pinned, unpinned
}

// releaseVersion reports whether m names a release a go.mod can require
// and a proxy can serve.
func releaseVersion(m module.Version) bool {
	return module.Check(m.Path, m.Version) == nil &&
		semver.Build(m.Version) == "" &&
		!module.IsPseudoVersion(m.Version)
}

// requireModules adds to the go.mod at path a requirement on every module of
// reqs it does not already require. One it requires is kept at its version:
// `go mod tidy` raises it if the rest needs a newer one, and init never
// lowers what a project chose.
func requireModules(path string, reqs []module.Version) (added, kept []module.Version, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		return nil, nil, err
	}
	have := map[string]string{}
	for _, r := range f.Require {
		have[r.Mod.Path] = r.Mod.Version
	}
	for _, r := range reqs {
		if v, ok := have[r.Path]; ok {
			kept = append(kept, module.Version{Path: r.Path, Version: v})
			continue
		}
		if err := f.AddRequire(r.Path, r.Version); err != nil {
			return nil, nil, err
		}
		added = append(added, r)
	}
	if len(added) == 0 {
		return nil, kept, nil
	}
	f.SortBlocks()
	f.Cleanup()
	out, err := f.Format()
	if err != nil {
		return nil, nil, err
	}
	return added, kept, os.WriteFile(path, out, 0o644)
}

// moduleList spells requirements for a message: "a v1, b v2".
func moduleList(ms []module.Version) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.Path + " " + m.Version
	}
	return strings.Join(parts, ", ")
}

// --- protoc-gen-go's embedded descriptor ---------------------------------------

// rawDescConst matches the file descriptor protoc-gen-go embeds in a .pb.go:
// a constant concatenated from one quoted string per line.
var rawDescConst = regexp.MustCompile(`(?m)^const (file_\w+_rawDesc) = "" \+\n((?:\t"(?:[^"\\\n]|\\.)*"(?: \+)?\n)+)`)

// rewriteRawDescGoPackage sets the go_package option of the descriptor a
// .pb.go embeds and writes the constant back the way protoc-gen-go does
// (split after every 0x0a byte, each piece %q-quoted), so the file is what
// generating it for goPackage writes. The descriptor is length-prefixed
// protobuf: replacing the path in the text would leave the lengths of the
// old one and a descriptor the runtime refuses at init.
func rewriteRawDescGoPackage(src []byte, goPackage string) ([]byte, error) {
	loc, name, raw, err := rawDesc(src)
	if err != nil {
		return nil, err
	}
	desc, err := setGoPackage(raw, goPackage)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("const " + name + " = \"\" +\n")
	pieces := bytes.SplitAfter(desc, []byte{'\n'})
	for i, p := range pieces {
		b.WriteString("\t" + strconv.Quote(string(p)))
		if i < len(pieces)-1 {
			b.WriteString(" +")
		}
		b.WriteString("\n")
	}
	out := append([]byte{}, src[:loc[0]]...)
	out = append(out, b.String()...)
	return append(out, src[loc[1]:]...), nil
}

// rawDesc finds the descriptor constant of a .pb.go and decodes it: where
// it is in src, its name, and its bytes.
func rawDesc(src []byte) (loc []int, name string, desc []byte, err error) {
	loc = rawDescConst.FindSubmatchIndex(src)
	if loc == nil {
		return nil, "", nil, fmt.Errorf("no raw descriptor constant")
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(src[loc[4]:loc[5]]), "\n"), "\n") {
		piece, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "\t"), " +"))
		if err != nil {
			return nil, "", nil, fmt.Errorf("the raw descriptor's line %q: %w", line, err)
		}
		desc = append(desc, piece...)
	}
	return loc, string(src[loc[2]:loc[3]]), desc, nil
}

// Field numbers of descriptor.proto: FileDescriptorProto.options and
// FileOptions.go_package.
const (
	fileOptionsField = 8
	goPackageField   = 11
)

// setGoPackage returns a FileDescriptorProto, in wire format, with the
// go_package of its options replaced. Every other field keeps its bytes and
// its place.
func setGoPackage(desc []byte, goPackage string) ([]byte, error) {
	fields, err := wireFields(desc)
	if err != nil {
		return nil, err
	}
	found := false
	var out []byte
	for _, f := range fields {
		if f.num != fileOptionsField || f.typ != 2 {
			out = append(out, f.raw...)
			continue
		}
		opts, err := wireFields(f.payload)
		if err != nil {
			return nil, fmt.Errorf("the file options: %w", err)
		}
		var body []byte
		for _, o := range opts {
			if o.num == goPackageField && o.typ == 2 {
				found = true
				body = appendLenField(body, goPackageField, []byte(goPackage))
				continue
			}
			body = append(body, o.raw...)
		}
		out = appendLenField(out, fileOptionsField, body)
	}
	if !found {
		return nil, fmt.Errorf("the descriptor sets no go_package")
	}
	return out, nil
}

type wireField struct {
	num     uint64
	typ     uint64 // 0 varint, 1 fixed64, 2 length-delimited, 5 fixed32
	payload []byte // the bytes of a length-delimited field
	raw     []byte // the whole field as encoded: tag, length, payload
}

func wireFields(b []byte) ([]wireField, error) {
	var out []wireField
	for i := 0; i < len(b); {
		start := i
		tag, n := uvarint(b[i:])
		if n == 0 {
			return nil, fmt.Errorf("a truncated tag at byte %d", i)
		}
		i += n
		f := wireField{num: tag >> 3, typ: tag & 7}
		switch f.typ {
		case 0:
			_, n = uvarint(b[i:])
			if n == 0 {
				return nil, fmt.Errorf("a truncated varint at byte %d", i)
			}
			i += n
		case 1:
			i += 8
		case 5:
			i += 4
		case 2:
			l, n := uvarint(b[i:])
			if n == 0 || uint64(len(b)-i-n) < l {
				return nil, fmt.Errorf("a truncated field at byte %d", i)
			}
			i += n
			f.payload = b[i : i+int(l)]
			i += int(l)
		default:
			return nil, fmt.Errorf("wire type %d at byte %d", f.typ, start)
		}
		if i > len(b) {
			return nil, fmt.Errorf("a truncated field at byte %d", start)
		}
		f.raw = b[start:i]
		out = append(out, f)
	}
	return out, nil
}

func uvarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

func appendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendLenField(b []byte, num uint64, payload []byte) []byte {
	b = appendUvarint(b, num<<3|2)
	b = appendUvarint(b, uint64(len(payload)))
	return append(b, payload...)
}
