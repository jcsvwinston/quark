// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertest

import (
	"database/sql/driver"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
)

// The kit's models. Every table starts with qk_kit_ so a database the kit
// shares with other tests is never touched outside its own tables, and each
// check has a table of its own so one failure does not cascade into the
// next check's setup.

// kitRow is the shape most query checks need: a generated key, a text and a
// number to filter and order by.
type kitRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
	N    int64  `db:"n"`
}

func (kitRow) TableName() string { return "qk_kit_rows" }

// kitLimit is ordered by n in the LimitOffset check.
type kitLimit struct {
	ID int64 `db:"id" pk:"true"`
	N  int64 `db:"n"`
}

func (kitLimit) TableName() string { return "qk_kit_limit" }

// kitKeys is read back by its generated key.
type kitKeys struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitKeys) TableName() string { return "qk_kit_keys" }

// kitQuote names its columns with words the engines reserve but Quark's
// identifier guard accepts — each one is an error on some engine unless the
// dialect quotes it.
type kitQuote struct {
	ID    int64  `db:"id" pk:"true"`
	User  string `db:"user,size=40"`
	Desc  string `db:"desc,size=40"`
	Check int64  `db:"check"`
	Key   string `db:"key,size=40"`
	Level int64  `db:"level"`
}

func (kitQuote) TableName() string { return "qk_kit_quote" }

// kitTrash carries Quark's soft-delete column, which a delete fills with
// Dialect.CurrentTimestamp().
type kitTrash struct {
	ID        int64      `db:"id" pk:"true"`
	Name      string     `db:"name,size=40"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func (kitTrash) TableName() string { return "qk_kit_trash" }

// kitDoc holds JSON as text.
type kitDoc struct {
	ID  int64  `db:"id" pk:"true"`
	Doc string `db:"doc,size=200"`
}

func (kitDoc) TableName() string { return "qk_kit_docs" }

// kitUpsert has the unique column an upsert conflicts on.
type kitUpsert struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code,size=20" quark:"unique"`
	Name string `db:"name,size=40"`
	N    int64  `db:"n"`
}

func (kitUpsert) TableName() string { return "qk_kit_upsert" }

// kitLock is locked row by row.
type kitLock struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitLock) TableName() string { return "qk_kit_lock" }

// kitSave is written inside savepoints.
type kitSave struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitSave) TableName() string { return "qk_kit_save" }

// kitAlter is changed column by column with the Dialect's ALTER methods.
type kitAlter struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitAlter) TableName() string { return "qk_kit_alter" }

// kitAuto is numbered by the engine.
type kitAuto struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitAuto) TableName() string { return "qk_kit_auto" }

// kitIdem declares a unique column and an index, so a second Migrate has a
// table and two indexes it must not create again.
type kitIdem struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code,size=20" quark:"unique"`
	Rank int64  `db:"rank" quark:"index"`
}

func (kitIdem) TableName() string { return "qk_kit_idem" }

// kitSchema is what IntrospectSchema has to read back faithfully.
type kitSchema struct {
	ID   int64   `db:"id" pk:"true"`
	Name string  `db:"name,size=40" nullable:"false"`
	Note *string `db:"note,size=40"`
	Code string  `db:"code,size=20" quark:"unique"`
	Rank int64   `db:"rank" quark:"index"`
}

func (kitSchema) TableName() string { return "qk_kit_schema" }

// kitPlanV1 and kitPlanV2 are one table at two versions: V2 adds a column
// and an index, which PlanMigration must propose and ApplyPlan must create.
type kitPlanV1 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitPlanV1) TableName() string { return "qk_kit_plan" }

type kitPlanV2 struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name,size=40"`
	Extra int64  `db:"extra" quark:"index"`
}

func (kitPlanV2) TableName() string { return "qk_kit_plan" }

// kitSyncV1 and kitSyncV2: Sync adds the column V2 declares.
type kitSyncV1 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitSyncV1) TableName() string { return "qk_kit_sync" }

type kitSyncV2 struct {
	ID    int64   `db:"id" pk:"true"`
	Name  string  `db:"name,size=40"`
	Added *string `db:"added,size=40"`
}

func (kitSyncV2) TableName() string { return "qk_kit_sync" }

// kitParent and kitChild carry the foreign key, check and index operations
// ApplyPlan writes.
type kitParent struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=40"`
}

func (kitParent) TableName() string { return "qk_kit_parent" }

type kitChild struct {
	ID       int64  `db:"id" pk:"true"`
	ParentID int64  `db:"parent_id"`
	Code     string `db:"code,size=20"`
	Qty      int64  `db:"qty"`
	Note     string `db:"note,size=20"`
}

func (kitChild) TableName() string { return "qk_kit_child" }

// --- one model per column kind, for ColumnTyper -------------------------------

type kitString struct {
	ID int64  `db:"id" pk:"true"`
	V  string `db:"v,size=40"`
}

func (kitString) TableName() string { return "qk_kit_t_string" }

type kitInt16 struct {
	ID int64 `db:"id" pk:"true"`
	V  int16 `db:"v"`
}

func (kitInt16) TableName() string { return "qk_kit_t_int16" }

type kitInt32 struct {
	ID int64 `db:"id" pk:"true"`
	V  int32 `db:"v"`
}

func (kitInt32) TableName() string { return "qk_kit_t_int32" }

type kitInt64 struct {
	ID int64 `db:"id" pk:"true"`
	V  int64 `db:"v"`
}

func (kitInt64) TableName() string { return "qk_kit_t_int64" }

type kitFloat32 struct {
	ID int64   `db:"id" pk:"true"`
	V  float32 `db:"v"`
}

func (kitFloat32) TableName() string { return "qk_kit_t_float32" }

type kitFloat64 struct {
	ID int64   `db:"id" pk:"true"`
	V  float64 `db:"v"`
}

func (kitFloat64) TableName() string { return "qk_kit_t_float64" }

type kitDecimal struct {
	ID int64   `db:"id" pk:"true"`
	V  float64 `db:"v,precision=12,scale=2"`
}

func (kitDecimal) TableName() string { return "qk_kit_t_decimal" }

type kitBool struct {
	ID int64 `db:"id" pk:"true"`
	V  bool  `db:"v"`
}

func (kitBool) TableName() string { return "qk_kit_t_bool" }

type kitBoolDefault struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=20"`
	V    bool   `db:"v" default:"true"`
}

func (kitBoolDefault) TableName() string { return "qk_kit_t_booldef" }

type kitTime struct {
	ID int64     `db:"id" pk:"true"`
	V  time.Time `db:"v"`
}

func (kitTime) TableName() string { return "qk_kit_t_time" }

type kitBytes struct {
	ID int64  `db:"id" pk:"true"`
	V  []byte `db:"v"`
}

func (kitBytes) TableName() string { return "qk_kit_t_bytes" }

type kitJSON struct {
	ID int64                         `db:"id" pk:"true"`
	V  quark.JSON[map[string]string] `db:"v"`
}

func (kitJSON) TableName() string { return "qk_kit_t_json" }

type kitUUIDRow struct {
	ID int64   `db:"id" pk:"true"`
	V  kitUUID `db:"v"`
}

func (kitUUIDRow) TableName() string { return "qk_kit_t_uuid" }

type kitIP struct {
	ID int64  `db:"id" pk:"true"`
	V  net.IP `db:"v"`
}

func (kitIP) TableName() string { return "qk_kit_t_ip" }

type kitArray struct {
	ID int64   `db:"id" pk:"true"`
	V  []int64 `db:"v"`
}

func (kitArray) TableName() string { return "qk_kit_t_array" }

type kitRange struct {
	ID int64              `db:"id" pk:"true"`
	V  quark.Range[int64] `db:"v"`
}

func (kitRange) TableName() string { return "qk_kit_t_range" }

type kitNullable struct {
	ID int64   `db:"id" pk:"true"`
	V  *string `db:"v,size=40"`
}

func (kitNullable) TableName() string { return "qk_kit_t_null" }

// kitUUID is the shape of google/uuid.UUID — a 16-byte array that travels as
// text through its own Valuer and Scanner — which Quark classifies as
// quarkdriver.KindUUID. Declared here so the kit needs no uuid module.
type kitUUID [16]byte

func (u kitUUID) Value() (driver.Value, error) {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}

func (u *kitUUID) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("kitUUID: cannot scan %T", src)
	}
	s = strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	if len(s) != 32 {
		return fmt.Errorf("kitUUID: %q is not a UUID", s)
	}
	for i := 0; i < 16; i++ {
		if _, err := fmt.Sscanf(s[2*i:2*i+2], "%02x", &u[i]); err != nil {
			return err
		}
	}
	return nil
}
