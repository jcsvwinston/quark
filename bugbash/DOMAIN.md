# Bug-bash domain — ERP-SaaS multi-tenant

> Dominio que el bug-bash usa para ejercitar Quark. **No es código todavía**
> — este documento es la **especificación**. Code lo traduce a structs Go
> bajo `bugbash/domain/*.go` en la primera sesión de implementación.
>
> Lectura recomendada antes de implementar: [`docs/BUGBASH_PLAN.md`](../docs/BUGBASH_PLAN.md).

## Por qué este dominio

Quark se vende por (a) cubrir 6 motores, (b) multi-tenancy seria con 4
estrategias, (c) tipos ricos, y (d) caché L2 + audit log para uso
empresarial. El dominio elegido es un **ERP-SaaS** porque:

- Tiene **multi-tenancy intrínseca** (cada cliente del SaaS es un
  tenant). Ejercita las 4 estrategias sin forzarlo.
- Combina **tipos ricos** (decimal para precios, UUID para external
  IDs, time.Time con TZ para auditoría legal, Array para tags, JSON
  para metadata).
- Cubre **las 5 relaciones**: belongs_to, has_one, has_many, m2m,
  polymorphic — todas con uso natural.
- Tiene **datos jerárquicos** (categorías padre/hijo) que ejercitan
  CTEs recursivos.
- Tiene **escalas asimétricas**: millones de `order_lines`, miles de
  `orders`, cientos de `users`, decenas de `organizations`. Buen
  caso para sharding y paginación.

**Lo que NO es:** un dominio "blog" o "todo" — esos son demasiado pobres
para ejercitar JSON, Array, decimal, polimorfismo, etc. en combinación.

## Entidades (15-20 tablas)

### 1. `organizations` (tenants)

```go
type Organization struct {
    ID         int64                  `db:"id" pk:"true"`
    UUID       uuid.UUID              `db:"uuid"`            // external ID via RegisterTypeMapper
    Name       string                 `db:"name" quark:"not_null,unique"`
    Slug       string                 `db:"slug" quark:"unique"`
    Plan       string                 `db:"plan"`            // "free" | "pro" | "enterprise"
    Settings   quark.JSON[OrgSettings] `db:"settings"`       // typed JSON
    CreatedAt  time.Time              `db:"created_at" quark:"tz=UTC"`
    UpdatedAt  time.Time              `db:"updated_at" quark:"tz=UTC"`
    DeletedAt  *time.Time             `db:"deleted_at" quark:"tz=UTC"` // soft delete
}

type OrgSettings struct {
    DefaultLocale  string   `json:"default_locale"`
    EnabledModules []string `json:"enabled_modules"`
    SLAHours       int      `json:"sla_hours"`
}
```

**Ejercita:** JSON[T], UUID (vía mapper), soft delete, `unique` constraints.

### 2. `users` (con polimorfismo en hasOne `profile`)

```go
type User struct {
    ID             int64               `db:"id" pk:"true"`
    OrganizationID int64               `db:"organization_id"`        // FK
    Email          string              `db:"email" quark:"unique"`
    PasswordHash   []byte              `db:"password_hash"`          // BLOB
    DisplayName    quark.Nullable[string] `db:"display_name"`
    Locale         string              `db:"locale"`
    Version        int                 `db:"version" quark:"version"` // optimistic lock
    LastLoginAt    *time.Time          `db:"last_login_at" quark:"tz=Europe/Madrid"`
    CreatedAt      time.Time           `db:"created_at" quark:"tz=UTC"`
    DeletedAt      *time.Time          `db:"deleted_at" quark:"tz=UTC"`

    Organization *Organization  `quark:"belongs_to:organization_id"`
    Profile      *UserProfile   `quark:"has_one:user_id"`
    Roles        []Role         `quark:"many_to_many:user_roles,user_id,role_id"`
}
```

**Ejercita:** belongs_to, has_one, m2m, Nullable[string], optimistic
locking, per-column TZ (`Europe/Madrid` distinto del default UTC), BLOB.

### 3. `user_profiles` (1:1 con users)

```go
type UserProfile struct {
    ID        int64                       `db:"id" pk:"true"`
    UserID    int64                       `db:"user_id" quark:"unique"`
    Bio       string                      `db:"bio"`              // TEXT
    Avatar    quark.Nullable[[]byte]      `db:"avatar"`           // BLOB nullable
    Prefs     quark.JSON[ProfilePrefs]    `db:"prefs"`
    Tags      quark.Array[string]         `db:"tags"`             // Array
    CreatedAt time.Time                   `db:"created_at" quark:"tz=UTC"`
}

type ProfilePrefs struct {
    Theme         string `json:"theme"`
    Notifications struct {
        Email bool `json:"email"`
        Push  bool `json:"push"`
    } `json:"notifications"`
}
```

**Ejercita:** Array[T], JSON[T] anidada, Nullable[[]byte], 1:1.

### 4. `roles` y `user_roles` (m2m)

```go
type Role struct {
    ID             int64                `db:"id" pk:"true"`
    OrganizationID int64                `db:"organization_id"`
    Name           string               `db:"name"`
    Permissions    quark.Array[string]  `db:"permissions"`
    CreatedAt      time.Time            `db:"created_at" quark:"tz=UTC"`
}

// user_roles es join table — definida en quark.JoinTable o como struct:
type UserRole struct {
    UserID    int64     `db:"user_id" pk:"true"`
    RoleID    int64     `db:"role_id" pk:"true"`
    GrantedAt time.Time `db:"granted_at" quark:"tz=UTC"`
}
```

**Ejercita:** m2m con metadata, composite PK.

### 5. `categories` (jerárquico, autorelacional)

```go
type Category struct {
    ID             int64    `db:"id" pk:"true"`
    OrganizationID int64    `db:"organization_id"`
    ParentID       *int64   `db:"parent_id"`     // self-FK, nullable for roots
    Name           string   `db:"name"`
    Slug           string   `db:"slug"`
    Depth          int      `db:"depth"`         // mantenido por trigger o app
    CreatedAt      time.Time `db:"created_at" quark:"tz=UTC"`

    Parent   *Category   `quark:"belongs_to:parent_id"`
    Children []Category  `quark:"has_many:parent_id"`
}
```

**Ejercita:** autoreferencial, **CTE recursivo** (para listar árbol),
nested preload `Children.Children.Children`.

### 6. `products`

```go
type Product struct {
    ID             int64                  `db:"id" pk:"true"`
    OrganizationID int64                  `db:"organization_id"`
    CategoryID     int64                  `db:"category_id"`
    SKU            string                 `db:"sku" quark:"unique"`
    Name           string                 `db:"name"`
    Description    string                 `db:"description"`     // TEXT
    Price          decimal.Decimal        `db:"price"`           // DECIMAL via mapper
    Currency       string                 `db:"currency"`        // ISO 4217
    Weight         decimal.Decimal        `db:"weight,precision=10,scale=3"`
    Active         bool                   `db:"active"`
    Tags           quark.Array[string]    `db:"tags"`
    Attrs          quark.JSON[ProductAttrs] `db:"attrs"`
    CreatedAt      time.Time              `db:"created_at" quark:"tz=UTC"`
    DeletedAt      *time.Time             `db:"deleted_at" quark:"tz=UTC"`

    Category *Category    `quark:"belongs_to:category_id"`
    Inventory []Inventory `quark:"has_many:product_id"`
}

type ProductAttrs struct {
    Color       string         `json:"color"`
    Size        string         `json:"size"`
    Dimensions  map[string]any `json:"dimensions"`
}
```

**Ejercita:** decimal.Decimal con precision/scale, JSON[T] con
`map[string]any`, soft delete, m2m con categories vía hasMany.

### 7. `warehouses` y `inventory`

```go
type Warehouse struct {
    ID             int64   `db:"id" pk:"true"`
    OrganizationID int64   `db:"organization_id"`
    Code           string  `db:"code" quark:"unique"`
    Location       string  `db:"location"`
    GeoLat         float64 `db:"geo_lat"`
    GeoLng         float64 `db:"geo_lng"`
    CreatedAt      time.Time `db:"created_at" quark:"tz=UTC"`
}

type Inventory struct {
    ID          int64   `db:"id" pk:"true"`
    ProductID   int64   `db:"product_id"`
    WarehouseID int64   `db:"warehouse_id"`
    Quantity    int     `db:"quantity"`
    Reserved    int     `db:"reserved"`
    Version     int     `db:"version" quark:"version"`  // optimistic lock para reservas
    UpdatedAt   time.Time `db:"updated_at" quark:"tz=UTC"`

    Product   *Product   `quark:"belongs_to:product_id"`
    Warehouse *Warehouse `quark:"belongs_to:warehouse_id"`
}
```

**Ejercita:** composite-key-like (product+warehouse uniqueness vía
index), `FOR UPDATE` / `SKIP LOCKED` en reservas concurrentes, optimistic
locking en escenario realista.

### 8. `customers`

```go
type Customer struct {
    ID             int64                  `db:"id" pk:"true"`
    OrganizationID int64                  `db:"organization_id"`
    UUID           uuid.UUID              `db:"uuid"`
    Name           string                 `db:"name"`
    Email          quark.Nullable[string] `db:"email"`
    Phone          quark.Nullable[string] `db:"phone"`
    Address        quark.JSON[Address]    `db:"address"`
    CreditLimit    decimal.Decimal        `db:"credit_limit"`
    TaxID          string                 `db:"tax_id"`
    Metadata       quark.JSON[map[string]any] `db:"metadata"`
    CreatedAt      time.Time              `db:"created_at" quark:"tz=UTC"`
    DeletedAt      *time.Time             `db:"deleted_at" quark:"tz=UTC"`

    Orders []Order `quark:"has_many:customer_id"`
}

type Address struct {
    Line1      string `json:"line1"`
    Line2      string `json:"line2"`
    City       string `json:"city"`
    State      string `json:"state"`
    PostalCode string `json:"postal_code"`
    Country    string `json:"country"`
}
```

**Ejercita:** JSON[T] (struct) + JSON[map[string]any] (raw), UUID.

### 9. `orders`

```go
type Order struct {
    ID             int64                  `db:"id" pk:"true"`
    OrganizationID int64                  `db:"organization_id"`
    CustomerID     int64                  `db:"customer_id"`
    Number         string                 `db:"number" quark:"unique"`
    Status         string                 `db:"status"`              // pending/paid/shipped/cancelled
    Subtotal       decimal.Decimal        `db:"subtotal"`
    Tax            decimal.Decimal        `db:"tax"`
    Total          decimal.Decimal        `db:"total"`
    Currency       string                 `db:"currency"`
    SLADuration    time.Duration          `db:"sla_duration"`        // Duration via mapper
    PlacedAt       time.Time              `db:"placed_at" quark:"tz=UTC"`
    ShippedAt      *time.Time             `db:"shipped_at" quark:"tz=UTC"`
    Notes          quark.Nullable[string] `db:"notes"`               // TEXT nullable
    CreatedAt      time.Time              `db:"created_at" quark:"tz=UTC"`
    DeletedAt      *time.Time             `db:"deleted_at" quark:"tz=UTC"`

    Customer *Customer    `quark:"belongs_to:customer_id"`
    Lines    []OrderLine  `quark:"has_many:order_id"`
    Payments []Payment    `quark:"has_many:order_id"`
}
```

**Ejercita:** time.Duration, *time.Time con TZ tag, decimal tax/total
con suma checkeable, nested preload `Order.Lines.Product.Category`.

### 10. `order_lines` (M:N efectiva entre Order y Product, con qty/price)

```go
type OrderLine struct {
    ID        int64           `db:"id" pk:"true"`
    OrderID   int64           `db:"order_id"`
    ProductID int64           `db:"product_id"`
    Quantity  int             `db:"quantity"`
    UnitPrice decimal.Decimal `db:"unit_price"`
    Discount  decimal.Decimal `db:"discount"`
    LineTotal decimal.Decimal `db:"line_total"`         // calculado
    Sequence  int             `db:"sequence"`            // orden en el order

    Order   *Order   `quark:"belongs_to:order_id"`
    Product *Product `quark:"belongs_to:product_id"`
}
```

**Ejercita:** la tabla de mayor volumen (5M filas en F4); ejercita
chunking IN cuando se eager-loadea, optimistic locking si el qty se
edita post-checkout.

### 11. `payments` y `refunds`

```go
type Payment struct {
    ID         int64                `db:"id" pk:"true"`
    OrderID    int64                `db:"order_id"`
    Method     string               `db:"method"`            // card/transfer/cash
    Amount     decimal.Decimal      `db:"amount"`
    Currency   string               `db:"currency"`
    Reference  string               `db:"reference"`
    Provider   string               `db:"provider"`
    Status     string               `db:"status"`
    ProcessedAt time.Time           `db:"processed_at" quark:"tz=UTC"`
    Metadata   quark.JSON[map[string]any] `db:"metadata"`
}

type Refund struct {
    ID         int64           `db:"id" pk:"true"`
    PaymentID  int64           `db:"payment_id"`
    Amount     decimal.Decimal `db:"amount"`
    Reason     string          `db:"reason"`
    ProcessedAt time.Time      `db:"processed_at" quark:"tz=UTC"`

    Payment *Payment `quark:"belongs_to:payment_id"`
}
```

**Ejercita:** hooks transaccionales (`BeforeUpdate` de Payment debe crear
audit row; `AfterUpdate` post-commit publica al EventBus).

### 12. `invoices` e `invoice_lines`

```go
type Invoice struct {
    ID             int64                  `db:"id" pk:"true"`
    OrganizationID int64                  `db:"organization_id"`
    OrderID        *int64                 `db:"order_id"`   // nullable: facturas sueltas
    Number         string                 `db:"number" quark:"unique"`
    IssuedAt       time.Time              `db:"issued_at" quark:"tz=Europe/Madrid"`
    DueAt          time.Time              `db:"due_at" quark:"tz=Europe/Madrid"`
    Status         string                 `db:"status"`
    Subtotal       decimal.Decimal        `db:"subtotal"`
    TaxRules       quark.Array[string]    `db:"tax_rules"`     // array de IDs
    Total          decimal.Decimal        `db:"total"`
    PDF            quark.Nullable[[]byte] `db:"pdf"`           // BLOB nullable

    Lines []InvoiceLine `quark:"has_many:invoice_id"`
    Order *Order        `quark:"belongs_to:order_id"`
}

type InvoiceLine struct {
    ID         int64           `db:"id" pk:"true"`
    InvoiceID  int64           `db:"invoice_id"`
    Description string         `db:"description"`
    Quantity   decimal.Decimal `db:"quantity"`
    UnitPrice  decimal.Decimal `db:"unit_price"`
    TaxRate    decimal.Decimal `db:"tax_rate"`
    Total      decimal.Decimal `db:"total"`
}
```

**Ejercita:** dos zonas horarias en la misma tabla (UTC vs Europe/Madrid),
Array de IDs externos.

### 13. `tax_rules`

```go
type TaxRule struct {
    ID             int64           `db:"id" pk:"true"`
    OrganizationID int64           `db:"organization_id"`
    Code           string          `db:"code" quark:"unique"`
    Name           string          `db:"name"`
    Rate           decimal.Decimal `db:"rate"`
    Country        string          `db:"country"`
    CategoryID     *int64          `db:"category_id"`   // nullable
    ValidFrom      time.Time       `db:"valid_from" quark:"tz=UTC"`
    ValidUntil     *time.Time      `db:"valid_until" quark:"tz=UTC"`
}
```

**Ejercita:** queries con rangos de fechas (`WHERE valid_from <= ? AND
(valid_until IS NULL OR valid_until > ?)`), índices compuestos.

### 14. `audit_events` (polimórfico)

```go
type AuditEvent struct {
    ID         int64                  `db:"id" pk:"true"`
    OrgID      int64                  `db:"organization_id"`
    ActorID    *int64                 `db:"actor_id"`        // nullable: system events
    SubjectType string                `db:"subject_type"`    // "Order" | "Invoice" | "User" | ...
    SubjectID  int64                  `db:"subject_id"`
    Action     string                 `db:"action"`          // created/updated/deleted/refunded
    Diff       quark.JSON[map[string]any] `db:"diff"`
    Metadata   quark.JSON[map[string]any] `db:"metadata"`
    OccurredAt time.Time              `db:"occurred_at" quark:"tz=UTC"`
}
```

**Ejercita:** polimorfismo Quark-style (`Subject` via type+id), volumen
alto (cada CRUD genera audit), índices por (subject_type, subject_id).

### 15. `attachments`

```go
type Attachment struct {
    ID         int64    `db:"id" pk:"true"`
    OrgID      int64    `db:"organization_id"`
    SubjectType string  `db:"subject_type"`
    SubjectID  int64    `db:"subject_id"`
    Filename   string   `db:"filename"`
    MimeType   string   `db:"mime_type"`
    SizeBytes  int64    `db:"size_bytes"`
    Content    []byte   `db:"content"`           // BLOB grande
    UploadedAt time.Time `db:"uploaded_at" quark:"tz=UTC"`
}
```

**Ejercita:** BLOBs grandes (1-10 MB cada uno) — testar streaming via
`Iter`/`Cursor` para no cargar todo a memoria. Polimorfismo cross-tabla.

### 16. `notes` (TEXT grande)

```go
type Note struct {
    ID         int64     `db:"id" pk:"true"`
    OrgID      int64     `db:"organization_id"`
    SubjectType string   `db:"subject_type"`
    SubjectID  int64     `db:"subject_id"`
    AuthorID   int64     `db:"author_id"`
    Body       string    `db:"body"`              // TEXT muy grande
    Pinned     bool      `db:"pinned"`
    CreatedAt  time.Time `db:"created_at" quark:"tz=UTC"`
}
```

**Ejercita:** TEXT grande (5-100 KB), `Where("body", "LIKE", "%palabra%")`
para forzar full-text-like en cada motor.

## Relaciones — resumen

```
Organization 1───* User
Organization 1───* Customer
Organization 1───* Product
Organization 1───* Category
Organization 1───* Warehouse
Organization 1───* Order      ───────── (tenant scope)
Organization 1───* Invoice
Organization 1───* AuditEvent

User 1───1 UserProfile
User *───* Role (via user_roles)

Category 1───* Category (parent_id, autoref)
Category 1───* Product

Product 1───* Inventory
Warehouse 1───* Inventory

Customer 1───* Order
Order 1───* OrderLine
OrderLine *───1 Product

Order 1───* Payment
Payment 1───* Refund
Order 1───? Invoice  (nullable: facturas sueltas también)
Invoice 1───* InvoiceLine

TaxRule  ←  referenciada por Invoice.TaxRules (Array de codes)

AuditEvent ───polimórfico──→ {Order, Invoice, User, Payment, ...}
Attachment ───polimórfico──→ {Order, Invoice, User, ...}
Note       ───polimórfico──→ {Order, Invoice, Customer, ...}
```

## Cardinalidad para F4 — volumen

| Tabla | Filas para F4 |
| --- | ---: |
| `organizations` | 100 |
| `users` | 100,000 |
| `user_profiles` | 100,000 |
| `roles` | 1,000 |
| `user_roles` | 300,000 |
| `categories` | 5,000 (con jerarquía hasta 5 niveles) |
| `products` | 50,000 |
| `warehouses` | 500 |
| `inventory` | 1,000,000 |
| `customers` | 200,000 |
| `orders` | 1,000,000 |
| `order_lines` | 5,000,000 |
| `payments` | 1,200,000 |
| `refunds` | 50,000 |
| `invoices` | 800,000 |
| `invoice_lines` | 4,000,000 |
| `tax_rules` | 10,000 |
| `audit_events` | 10,000,000 |
| `attachments` | 500,000 (~5MB cada uno = 2.5 TB total — solo en F14 soak; en F4 se siembran 5k) |
| `notes` | 2,000,000 |

**Total agregado:** ≥25M filas en F4 (sin attachments grandes). Cabe en
Postgres/MySQL/MSSQL/Oracle/MariaDB con discos razonables; SQLite tarda
más pero entra.

## Seed determinista

`bugbash/seed/seed.go` debe garantizar:

- Mismo `--seed=N` → mismo dataset byte a byte.
- Distribución realista: 80% de orders pertenecen a 20% de customers
  (Pareto).
- Fechas en `created_at` distribuidas a lo largo de 3 años con weighting
  reciente (60% en último año).
- Status de orders: 5% cancelled, 70% paid+shipped, 15% pending, 10% paid.
- Productos con precios en distribución log-normal (mucho barato, pocos
  caros).
- 90% de categorías son leaves; 10% son internas.

El seed se genera con `bugbash/seed/seed.go` invocado al inicio de F1.
**No regenerar** entre fases consecutivas — los datos persisten salvo
que la fase lo declare explícitamente.

## Cómo Code debería implementar este dominio

Primera sesión: crear `bugbash/go.mod` (módulo independiente con
`replace github.com/jcsvwinston/quark => ../`), generar cada `*.go` del
dominio siguiendo este documento literalmente, correr `quark gen
./bugbash/domain/` y verificar que compila.

Segunda sesión: `bugbash/seed/seed.go` con generadores deterministas y
las cardinalidades de F4.

Tercera sesión y siguientes: una fase por sesión, con `code-reviewer`
antes de cada PR. Las fases F0-F2 son foundation; F3-F13 son ataque;
F14 es soak.
