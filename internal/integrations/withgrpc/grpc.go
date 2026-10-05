// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package withgrpc serves the notes API as a gRPC service (notespb, generated
// from notespb/notes.proto). Every method receives the call's context — the
// one gRPC cancels when the client goes away or its deadline passes — and
// returns Quark's errors as they come; one unary interceptor gives each its
// status code.
package withgrpc

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/jcsvwinston/quark"

	// The driver module: registers the database/sql driver and the
	// classifier behind quark.IsUniqueViolation.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"

	"github.com/jcsvwinston/quark/internal/integrations/notes"
	"github.com/jcsvwinston/quark/internal/integrations/withgrpc/notespb"
)

// NewServer registers the notes service on a gRPC server whose interceptor
// maps Quark's errors to status codes. opts come first, so an interceptor
// the caller passes runs outside that one.
func NewServer(client *quark.Client, opts ...grpc.ServerOption) *grpc.Server {
	srv := grpc.NewServer(append(opts, grpc.ChainUnaryInterceptor(quarkStatus))...)
	notespb.RegisterNotesServer(srv, &service{client: client})
	return srv
}

type service struct {
	notespb.UnimplementedNotesServer
	client *quark.Client
}

func (s *service) List(ctx context.Context, _ *notespb.ListRequest) (*notespb.NoteList, error) {
	list, err := quark.For[notes.Note](ctx, s.client).OrderBy("id", "DESC").Limit(100).List()
	if err != nil {
		return nil, err
	}
	return toList(list), nil
}

func (s *service) Get(ctx context.Context, req *notespb.GetRequest) (*notespb.Note, error) {
	n, err := quark.For[notes.Note](ctx, s.client).Find(req.GetId())
	if err != nil {
		return nil, err
	}
	return toNote(n), nil
}

func (s *service) Create(ctx context.Context, d *notespb.Draft) (*notespb.Note, error) {
	n, err := notes.Draft{Title: d.GetTitle(), Body: d.GetBody()}.Note()
	if err == nil {
		err = quark.For[notes.Note](ctx, s.client).Create(&n)
	}
	if err != nil {
		return nil, err
	}
	return toNote(n), nil
}

func (s *service) Import(ctx context.Context, req *notespb.ImportRequest) (*notespb.NoteList, error) {
	if len(req.GetDrafts()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "no notes to import")
	}
	created := make([]notes.Note, 0, len(req.GetDrafts()))
	err := s.client.Tx(ctx, func(tx *quark.Tx) error {
		for _, d := range req.GetDrafts() {
			n, err := notes.Draft{Title: d.GetTitle(), Body: d.GetBody()}.Note()
			if err == nil {
				err = quark.ForTx[notes.Note](ctx, tx).Create(&n)
			}
			if err != nil {
				return err // rolls back every note written before it
			}
			created = append(created, n)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return toList(created), nil
}

// Code is the gRPC code for an error a method meets: the same
// classification notes.Status turns into an HTTP status.
func Code(err error) codes.Code {
	switch {
	case errors.Is(err, notes.ErrNoTitle):
		return codes.InvalidArgument
	case errors.Is(err, quark.ErrNotFound):
		return codes.NotFound
	case quark.IsUniqueViolation(err):
		return codes.AlreadyExists
	case errors.Is(err, quark.ErrTimeout):
		return codes.DeadlineExceeded
	default:
		return codes.Internal
	}
}

// quarkStatus gives an error a method returns its code, unless it already
// carries one. The message is the code's name: a driver's text can name
// tables and columns, and it belongs in a log, not on the wire.
func quarkStatus(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	resp, err := handler(ctx, req)
	if err == nil {
		return resp, nil
	}
	if _, ok := status.FromError(err); ok {
		return nil, err
	}
	code := Code(err)
	return nil, status.Error(code, code.String())
}

func toNote(n notes.Note) *notespb.Note {
	return &notespb.Note{Id: n.ID, Title: n.Title, Body: n.Body, CreatedAt: timestamppb.New(n.CreatedAt)}
}

func toList(list []notes.Note) *notespb.NoteList {
	out := &notespb.NoteList{Notes: make([]*notespb.Note, 0, len(list))}
	for _, n := range list {
		out.Notes = append(out.Notes, toNote(n))
	}
	return out
}
