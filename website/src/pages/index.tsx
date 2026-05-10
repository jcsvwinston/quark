import type {ReactNode} from 'react';
import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';

import styles from './index.module.css';

// ─── Icons ─────────────────────────────────────────────────────────────────

function ArrowIcon() {
  return (
    <svg aria-hidden="true" viewBox="0 0 16 16" className={styles.btnIcon}>
      <path d="M6 3l5 5-5 5M2 8h8" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg aria-hidden="true" viewBox="0 0 18 18" className={styles.checkIcon}>
      <path d="M4 9.3l3 3L14 5.8" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function XIcon() {
  return (
    <svg aria-hidden="true" viewBox="0 0 18 18" className={styles.xIcon}>
      <path d="M5 5l8 8M13 5l-8 8" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

function PartialIcon() {
  return (
    <svg aria-hidden="true" viewBox="0 0 18 18" className={styles.partialIcon}>
      <path d="M4 9h10" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

// ─── Data ──────────────────────────────────────────────────────────────────

const stats = [
  { value: '6', label: 'SQL dialects' },
  { value: '0', label: 'code generation' },
  { value: '100%', label: 'Go generics' },
  { value: 'L2', label: 'built-in cache' },
  { value: 'OTel', label: 'native tracing' },
];

const features = [
  {
    icon: '⚡',
    title: 'Immutable query builder',
    description: 'Every builder method returns a new clone. Compose base queries and reuse them across goroutines without defensive copies.',
    link: '/docs/guides/querying',
  },
  {
    icon: '🔒',
    title: 'SQLGuard runtime safety',
    description: 'Column identifiers, table names, and operators are validated before SQL is assembled. Injection vectors caught at the ORM layer.',
    link: '/docs/reference/architecture',
  },
  {
    icon: '🌐',
    title: 'Six dialects, one API',
    description: 'PostgreSQL, MySQL, MariaDB, SQLite, MSSQL, and Oracle. Placeholders, DDL, upsert syntax, and pagination differ — your code does not.',
    link: '/docs/reference/dialects',
  },
  {
    icon: '📦',
    title: 'Batch operations',
    description: 'CreateBatch, UpsertBatch, UpdateBatch, DeleteBatch. Dialect-specific SQL generation and automatic chunking for Oracle IN-list limits.',
    link: '/docs/guides/batch-operations',
  },
  {
    icon: '🏢',
    title: 'Multi-tenancy built in',
    description: 'TenantRouter supports database-per-tenant, schema isolation, and row-level security from a single context key — no manual WHERE clauses.',
    link: '/docs/advanced/multi-tenant',
  },
  {
    icon: '📡',
    title: 'Production-grade hooks',
    description: 'Lifecycle hooks, middleware, query observers, L2 cache stores with tag invalidation, and OpenTelemetry spans — all plugged into the same client.',
    link: '/docs/advanced/caching-observability',
  },
];

type CompareValue = 'yes' | 'no' | 'partial' | string;

const comparison: { feature: string; quark: CompareValue; gorm: CompareValue; sqlx: CompareValue; ent: CompareValue }[] = [
  { feature: 'Go generics API',              quark: 'yes',     gorm: 'partial', sqlx: 'no',      ent: 'yes'     },
  { feature: 'SQL injection guard (ident.)', quark: 'yes',     gorm: 'no',      sqlx: 'no',      ent: 'no'      },
  { feature: 'Immutable query builder',      quark: 'yes',     gorm: 'no',      sqlx: 'N/A',     ent: 'yes'     },
  { feature: 'Zero code generation',         quark: 'yes',     gorm: 'yes',     sqlx: 'yes',     ent: 'no'      },
  { feature: '6 SQL dialects',               quark: 'yes',     gorm: 'yes',     sqlx: 'no',      ent: 'partial' },
  { feature: 'Multi-tenancy built in',       quark: 'yes',     gorm: 'no',      sqlx: 'no',      ent: 'no'      },
  { feature: 'Batch ops (4)',                quark: 'yes',     gorm: 'partial', sqlx: 'no',      ent: 'partial' },
  { feature: 'L2 cache + invalidation',      quark: 'yes',     gorm: 'no',      sqlx: 'no',      ent: 'no'      },
  { feature: 'Native OTel middleware',       quark: 'yes',     gorm: 'partial', sqlx: 'no',      ent: 'partial' },
  { feature: 'stdlib *sql.DB pool',          quark: 'yes',     gorm: 'yes',     sqlx: 'yes',     ent: 'no'      },
];

const productionChecklist = [
  { label: 'quark.For[T](ctx, client) — generics-based entry point' },
  { label: 'Middleware chain: wrap Query, QueryRow, and Exec independently' },
  { label: 'Lifecycle hooks: BeforeCreate, AfterUpdate, AfterDelete, …' },
  { label: 'L2 cache stores with tag-based cache invalidation on writes' },
  { label: 'TenantRouter: RLS, schema, and database-per-tenant strategies' },
  { label: 'OpenTelemetry spans, query observers, and structured events' },
];

// ─── Components ────────────────────────────────────────────────────────────

function CellValue({ v }: { v: CompareValue }) {
  if (v === 'yes')     return <span className={styles.cellYes}><CheckIcon /></span>;
  if (v === 'no')      return <span className={styles.cellNo}><XIcon /></span>;
  if (v === 'partial') return <span className={styles.cellPartial}><PartialIcon /></span>;
  return <span className={styles.cellText}>{v}</span>;
}

function Hero() {
  const logoSrc = useBaseUrl('/img/quark-logo.svg');
  return (
    <header className={styles.hero}>
      <div className={styles.heroCopy}>
        <div className={styles.heroBadge}>For Go · Open Source · Apache 2.0</div>
        <img src={logoSrc} alt="" className={styles.heroLogo} />
        <h1>
          The ORM for Go<br />
          <span className={styles.heroAccent}>that gets out of your way.</span>
        </h1>
        <p>
          Generics-based, immutable query builders, six SQL dialects, and
          production primitives like multi-tenancy, L2 caching, and OpenTelemetry
          — without code generation or global state.
        </p>
        <div className={styles.heroActions}>
          <Link className={styles.primaryButton} to="/docs/guides/getting-started">
            Get started <ArrowIcon />
          </Link>
          <Link className={styles.secondaryButton} to="/docs/intro">
            Read the docs
          </Link>
        </div>
        <div className={styles.dialectRow} aria-label="Supported dialects">
          {['PostgreSQL','MySQL','MariaDB','SQLite','MSSQL','Oracle'].map(d => (
            <span key={d}>{d}</span>
          ))}
        </div>
      </div>

      <div className={styles.heroVisual} aria-hidden="true">
        <div className={styles.windowBar}>
          <span className={styles.dot} style={{background:'#ec6a5f'}} />
          <span className={styles.dot} style={{background:'#f4bf4f'}} />
          <span className={styles.dot} style={{background:'#61c554'}} />
          <strong className={styles.windowTitle}>main.go</strong>
        </div>
        <pre className={styles.heroCode}>
          <code>{`// Connect
client, _ := quark.New(db,
    quark.WithDialect(quark.PostgreSQL()))

// Query
users, _ := quark.For[User](ctx, client).
    Where("active", "=", true).
    WhereIn("role", []any{"admin","editor"}).
    Preload("Posts", "Team").
    OrderBy("created_at", "DESC").
    Paginate(20, 0)

// Batch upsert
quark.For[Product](ctx, client).UpsertBatch(
    feed,
    []string{"sku"},
    []string{"price","stock"},
)`}</code>
        </pre>
        <div className={styles.queryFlow}>
          <span>generics API</span>
          <span>SQLGuard</span>
          <span>dialect SQL</span>
          <span>mapped structs</span>
        </div>
      </div>
    </header>
  );
}

function StatsBar() {
  return (
    <div className={styles.statsBar} aria-label="QUARK by the numbers">
      {stats.map(s => (
        <div className={styles.statItem} key={s.label}>
          <strong>{s.value}</strong>
          <span>{s.label}</span>
        </div>
      ))}
    </div>
  );
}

function FeatureGrid() {
  return (
    <section className={styles.featuresSection}>
      <div className={styles.sectionHeader}>
        <h2>Everything you need at the data layer.</h2>
        <p>Six core pillars that cover the full lifecycle of a production Go service.</p>
      </div>
      <div className={styles.featuresGrid}>
        {features.map(f => (
          <Link className={styles.featureCard} to={f.link} key={f.title}>
            <div className={styles.featureIcon}>{f.icon}</div>
            <h3>{f.title}</h3>
            <p>{f.description}</p>
            <span className={styles.featureLink}>Learn more <ArrowIcon /></span>
          </Link>
        ))}
      </div>
    </section>
  );
}

function ComparisonTable() {
  return (
    <section className={styles.compareSection}>
      <div className={styles.sectionHeader}>
        <h2>How QUARK compares.</h2>
        <p>QUARK occupies the space between GORM's pragmatism and Ent's rigour — without code generation.</p>
      </div>
      <div className={styles.tableWrapper}>
        <table className={styles.compareTable}>
          <thead>
            <tr>
              <th>Feature</th>
              <th className={styles.quarkCol}>QUARK</th>
              <th>GORM</th>
              <th>sqlx</th>
              <th>Ent</th>
            </tr>
          </thead>
          <tbody>
            {comparison.map(row => (
              <tr key={row.feature}>
                <td>{row.feature}</td>
                <td className={styles.quarkCol}><CellValue v={row.quark} /></td>
                <td><CellValue v={row.gorm} /></td>
                <td><CellValue v={row.sqlx} /></td>
                <td><CellValue v={row.ent} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className={styles.tableNote}>
        ✓ Full support &nbsp;·&nbsp; — Partial / plugin-required &nbsp;·&nbsp; ✗ Not available
      </p>
      <p className={styles.tableNote}>
        ¹ GORM v2 core API uses <code>interface{'{}'}</code>; generic wrappers exist but are not the primary API.
        &nbsp;·&nbsp;
        ² GORM &amp; ent protect <em>values</em> via parameterized queries; Quark additionally validates <em>identifiers</em> (column/table names). See <a href="/docs/reference/sqlguard">SQLGuard</a>.
        &nbsp;·&nbsp;
        ³ GORM queries mutate shared state when chained; <code>Session(&#123;&gorm.Session&#123;NewDB: true&#125;&#125;)</code> mitigates but is opt-in.
        &nbsp;·&nbsp;
        ⁴ GORM supports <code>CreateInBatches</code>; batch DELETE &amp; UPDATE require custom loops.
      </p>
      <p className={styles.tableNote}>
        <a href="/docs/reference/comparison">Cell-by-cell justification with code examples →</a>
      </p>
    </section>
  );
}

function WhyIBuiltThis() {
  return (
    <section className={styles.productionSection}>
      <div className={styles.productionCopy}>
        <h2>Why I built this.</h2>
        <p>
          After running production services on GORM, four patterns kept causing incidents:
        </p>
        <ul>
          <li>Every <code>db.Find(&amp;result)</code> forced an <code>interface&#123;&#125;</code> cast the compiler couldn't verify.</li>
          <li>Column names in <code>WHERE</code> clauses were plain strings with no guard against typos or injection in dynamic queries.</li>
          <li>N+1 queries appeared silently whenever a <code>Preload</code> was forgotten, only surfacing in slow-query logs hours later.</li>
          <li>Multi-tenant isolation meant copy-pasting <code>WHERE tenant_id = ?</code> everywhere, relying on discipline instead of enforcement.</li>
        </ul>
        <p>
          Quark is the ORM I wished existed: generics end the casts, SQLGuard validates every identifier at the API boundary, eager loading is explicit, and multi-tenancy is first-class — not an afterthought.
        </p>
      </div>
    </section>
  );
}

function TryLocally() {
  return (
    <section className={styles.docsNavSection}>
      <div className={styles.sectionHeader}>
        <h2>Try it locally.</h2>
        <p>Clone the repo and run the blog-api example in under a minute.</p>
      </div>
      <pre className={styles.heroCode} style={{maxWidth: '640px', margin: '0 auto'}}>
        <code>{`git clone https://github.com/jcsvwinston/quark
go run ./examples/blog-api
curl -s -X POST http://localhost:8080/authors \\
  -H "Content-Type: application/json" \\
  -d '{"name":"Alice","email":"alice@example.com"}' | jq .
curl -s "http://localhost:8080/posts" | jq .`}</code>
      </pre>
    </section>
  );
}

function ProductionSection() {
  return (
    <section className={styles.productionSection}>
      <div className={styles.productionCopy}>
        <h2>Grows with your application.</h2>
        <p>
          QUARK keeps the everyday path — query, create, update, delete — minimal
          and zero-surprise. When your service graduates to multi-tenancy, cache
          warming, or distributed tracing, those capabilities are already built in
          and ready to attach.
        </p>
        <Link className={styles.textLink} to="/docs/advanced/caching-observability">
          Explore production features <ArrowIcon />
        </Link>
      </div>
      <ul className={styles.productionList}>
        {productionChecklist.map(item => (
          <li key={item.label}>
            <CheckIcon />
            <span>{item.label}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function DocsNav() {
  const links = [
    { to: '/docs/guides/getting-started', label: '🚀 Quickstart' },
    { to: '/docs/guides/batch-operations', label: '📦 Batch Operations' },
    { to: '/docs/guides/relations', label: '🔗 Relations' },
    { to: '/docs/guides/migrations', label: '🔄 Migrations' },
    { to: '/docs/guides/cli', label: '🧰 Ops Workflows' },
    { to: '/docs/advanced/multi-tenant', label: '🏢 Multi-tenant' },
    { to: '/docs/advanced/caching-observability', label: '📡 Observability' },
    { to: '/docs/reference/sqlguard', label: '🔒 SQLGuard' },
    { to: '/docs/reference/comparison', label: '📊 Comparison' },
    { to: '/docs/reference/configuration', label: '⚙️ Configuration' },
    { to: '/docs/reference/dialects', label: '🌐 Dialects' },
    { to: '/docs/reference/benchmarks', label: '⚡ Benchmarks' },
    { to: '/docs/reference/release-notes', label: '📝 Release Notes' },
  ];
  return (
    <section className={styles.docsNavSection}>
      <div className={styles.sectionHeader}>
        <h2>Jump into the docs.</h2>
        <p>Organized around how teams adopt an ORM — model, query, evolve, scale.</p>
      </div>
      <div className={styles.docsNavGrid}>
        {links.map(l => (
          <Link to={l.to} key={l.to} className={styles.docsNavCard}>
            {l.label}
          </Link>
        ))}
      </div>
    </section>
  );
}

// ─── Page ──────────────────────────────────────────────────────────────────

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout title={siteConfig.title} description={siteConfig.tagline}>
      <main className={styles.page}>
        <Hero />
        <StatsBar />
        <WhyIBuiltThis />
        <FeatureGrid />
        <ComparisonTable />
        <ProductionSection />
        <TryLocally />
        <DocsNav />
      </main>
    </Layout>
  );
}
