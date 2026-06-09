package exercise

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

var relSeq int64

// RELATIONS ejerce Preload en las tres formas: belongs_to (con el caso BB-5 de
// FK nullable que NO debe cargar basura), has_many, y many_to_many con
// persistencia de asociación (Create de un Project con Tags inserta en la tabla
// join).
var RELATIONS = Exerciser{Name: "relations", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, _ Conn) error {
	rec.Note(QF("For"), QM("Preload"), QM("Where"))
	n := atomic.AddInt64(&relSeq, 1)

	// --- Semilla: owner + project + task(con assignee) + task(sin assignee) ---
	owner := &domain.Account{Email: fmt.Sprintf("rel%d@superapp.test", n), Name: "rel", Role: "member", Active: true}
	if err := quark.For[domain.Account](ctx, client).Create(owner); err != nil {
		return fmt.Errorf("seed owner: %w", err)
	}
	proj := &domain.Project{OwnerID: owner.ID, Name: "rel-proj", Status: "active"}
	if err := quark.For[domain.Project](ctx, client).Create(proj); err != nil {
		return fmt.Errorf("seed project: %w", err)
	}
	aid := owner.ID
	assigned := &domain.Task{ProjectID: proj.ID, Title: "assigned", AssigneeID: &aid}
	if err := quark.For[domain.Task](ctx, client).Create(assigned); err != nil {
		return fmt.Errorf("seed assigned task: %w", err)
	}
	unassigned := &domain.Task{ProjectID: proj.ID, Title: "unassigned"} // AssigneeID nil
	if err := quark.For[domain.Task](ctx, client).Create(unassigned); err != nil {
		return fmt.Errorf("seed unassigned task: %w", err)
	}

	// --- belongs_to: Task.Project + Task.Assignee ---
	t1, err := quark.For[domain.Task](rec.Mark(ctx, QM("First")), client).
		Preload("Project").Preload("Assignee").Where("id", "=", assigned.ID).First()
	if err != nil {
		return fmt.Errorf("preload belongs_to: %w", err)
	}
	if t1.Project == nil || t1.Project.ID != proj.ID {
		return fmt.Errorf("belongs_to Project no cargado: %+v", t1.Project)
	}
	if t1.Assignee == nil || t1.Assignee.ID != owner.ID {
		return fmt.Errorf("belongs_to Assignee no cargado: %+v", t1.Assignee)
	}

	// BB-5: una FK nullable a nil debe quedar nil, no cargar basura.
	t2, err := quark.For[domain.Task](ctx, client).Preload("Assignee").Where("id", "=", unassigned.ID).First()
	if err != nil {
		return fmt.Errorf("preload nullable FK: %w", err)
	}
	if t2.Assignee != nil {
		return fmt.Errorf("BB-5: Assignee debía quedar nil, cargó %+v", t2.Assignee)
	}

	// --- has_many: Account.Projects + Project.Tasks ---
	acc, err := quark.For[domain.Account](ctx, client).Preload("Projects").Where("id", "=", owner.ID).First()
	if err != nil {
		return fmt.Errorf("preload has_many Projects: %w", err)
	}
	if len(acc.Projects) < 1 {
		return fmt.Errorf("has_many Projects vacío")
	}
	p, err := quark.For[domain.Project](ctx, client).Preload("Tasks").Where("id", "=", proj.ID).First()
	if err != nil {
		return fmt.Errorf("preload has_many Tasks: %w", err)
	}
	if len(p.Tasks) != 2 {
		return fmt.Errorf("has_many Tasks=%d, esperaba 2", len(p.Tasks))
	}

	// --- many_to_many: crear un Project CON Tags (persistencia de asociación) ---
	mProj := &domain.Project{
		OwnerID: owner.ID, Name: "m2m-proj", Status: "active",
		Tags: []domain.Tag{
			{Slug: fmt.Sprintf("rel-tag-a-%d", n)},
			{Slug: fmt.Sprintf("rel-tag-b-%d", n)},
		},
	}
	if err := quark.For[domain.Project](rec.Mark(ctx, QM("Create")), client).Create(mProj); err != nil {
		return fmt.Errorf("m2m create con tags: %w", err)
	}
	mp, err := quark.For[domain.Project](ctx, client).Preload("Tags").Where("id", "=", mProj.ID).First()
	if err != nil {
		return fmt.Errorf("preload m2m Tags: %w", err)
	}
	if len(mp.Tags) != 2 {
		return fmt.Errorf("m2m Tags=%d, esperaba 2", len(mp.Tags))
	}

	return nil
}}
