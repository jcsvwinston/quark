// Package domain define el modelo de la superapp. Las entidades se eligen para
// FORZAR la amplitud de la API de Quark, no por valor de producto: relaciones
// (has_many/belongs_to/many_to_many), soft-delete, optimistic lock, PK
// compuesta, []byte y los tipos ricos (JSON[T]/Array[T]/Nullable[T]/tz).
//
// Tags verificados contra website/docs/guides/modeling.mdx.
package domain

import (
	"context"
	"time"

	"github.com/jcsvwinston/quark"
)

// AccountPrefs es el payload de la columna JSON de Account.
type AccountPrefs struct {
	Theme  string `json:"theme"`
	Locale string `json:"locale"`
}

// Account ejerce: tipos ricos (Nullable/JSON/Array), tz por columna,
// optimistic lock (version), soft delete (deleted_at), validación tag+método,
// y hooks Before*.
type Account struct {
	ID        int64                     `db:"id" pk:"true"`
	Email     string                    `db:"email" quark:"unique,not_null" validate:"required,email"`
	Name      string                    `db:"name" quark:"not_null"`
	Role      string                    `db:"role" default:"'member'" validate:"oneof=admin member viewer"`
	Active    bool                      `db:"active" default:"1"`
	Bio       quark.Nullable[string]    `db:"bio,size=512"`
	Settings  quark.JSON[AccountPrefs]  `db:"settings"`
	Tags      quark.Array[string]       `db:"tags"`
	LastLogin quark.Nullable[time.Time] `db:"last_login" quark:"tz=Europe/Madrid"`
	CreatedAt time.Time                 `db:"created_at"`
	UpdatedAt time.Time                 `db:"updated_at"`
	DeletedAt *time.Time                `db:"deleted_at"`
	Version   int64                     `db:"version" quark:"version"`

	// Relación (no es columna).
	Projects []Project `rel:"has_many" join:"owner_id"`
}

// Validate ejerce la validación a nivel método (se llama antes de la de tags).
func (a *Account) Validate(ctx context.Context) error { return nil }

func (a *Account) BeforeCreate(ctx context.Context) error {
	now := time.Now().UTC()
	a.CreatedAt, a.UpdatedAt = now, now
	return nil
}

func (a *Account) BeforeUpdate(ctx context.Context) error {
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// Project ejerce belongs_to (Owner), has_many (Tasks), many_to_many (Tags) y
// soft delete.
type Project struct {
	ID        int64      `db:"id" pk:"true"`
	OwnerID   int64      `db:"owner_id" quark:"not_null"`
	Name      string     `db:"name" quark:"not_null"`
	Status    string     `db:"status" default:"'draft'"`
	CreatedAt time.Time  `db:"created_at"`
	DeletedAt *time.Time `db:"deleted_at"`

	Owner *Account `rel:"belongs_to" join:"owner_id"`
	Tasks []Task   `rel:"has_many" join:"project_id"`
	// m2m: confirmar tags exactos (through/join) contra
	// website/docs/guides/relations.mdx en el slice de exercisers.
	Tags []Tag `rel:"many_to_many" join:"project_id"`
}

// Task ejerce belongs_to + una FK NULLABLE (*int64) que Preload debe seguir sin
// cargar basura — el caso del fix BB-5.
type Task struct {
	ID         int64                     `db:"id" pk:"true"`
	ProjectID  int64                     `db:"project_id" quark:"not_null"`
	Title      string                    `db:"title" quark:"not_null"`
	Done       bool                      `db:"done" default:"0"`
	AssigneeID *int64                    `db:"assignee_id"` // FK nullable (BB-5)
	DueAt      quark.Nullable[time.Time] `db:"due_at"`
	Priority   int                       `db:"priority" default:"0"`

	Project  *Project `rel:"belongs_to" join:"project_id"`
	Assignee *Account `rel:"belongs_to" join:"assignee_id"`
}

// Tag es el otro lado del m2m con Project.
type Tag struct {
	ID   int64  `db:"id" pk:"true"`
	Slug string `db:"slug" quark:"unique,not_null"`
}

// Membership ejerce PK COMPUESTA (account_id, project_id) y TableName().
type Membership struct {
	AccountID int64     `db:"account_id" pk:"true"`
	ProjectID int64     `db:"project_id" pk:"true"`
	Role      string    `db:"role" default:"'member'"`
	JoinedAt  time.Time `db:"joined_at"`
}

func (Membership) TableName() string { return "account_project_memberships" }

// Attachment ejerce binario []byte y Nullable[[]byte] (el caso del fix BB-6 de
// MSSQL: NULL []byte sobre nvarchar/varbinary).
type Attachment struct {
	ID       int64                  `db:"id" pk:"true"`
	TaskID   int64                  `db:"task_id" quark:"not_null"`
	Name     string                 `db:"name" quark:"not_null"`
	Bytes    []byte                 `db:"bytes"`
	Optional quark.Nullable[[]byte] `db:"optional"`
}

// AllModels devuelve los modelos en orden de migración (respeta dependencias FK).
func AllModels() []any {
	return []any{
		&Account{}, &Project{}, &Tag{}, &Task{},
		&Membership{}, &Attachment{},
	}
}
