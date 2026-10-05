// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package withgrpc

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
	"github.com/jcsvwinston/quark/internal/integrations/notestest"
	"github.com/jcsvwinston/quark/internal/integrations/withgrpc/notespb"
)

// serve runs srv on a loopback listener and returns a client for it.
func serve(t *testing.T, srv *grpc.Server) notespb.NotesClient {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return notespb.NewNotesClient(conn)
}

// expired is notestest.Expired for gRPC: every call reaches the method with
// a deadline that has already passed.
func expired(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	return handler(ctx, req)
}

func wantCode(t *testing.T, what string, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("%s: code %s (%v), want %s", what, got, err, want)
	}
}

// The HTTP battery, call for call, with gRPC codes for statuses.
func TestNotesService(t *testing.T) {
	client := notestest.Client(t)
	c := serve(t, NewServer(client))
	ctx := context.Background()

	list, err := c.List(ctx, &notespb.ListRequest{})
	if err != nil || len(list.GetNotes()) != 0 {
		t.Fatalf("List on an empty table: %v, %v", list, err)
	}

	first, err := c.Create(ctx, &notespb.Draft{Title: "first", Body: "hello"})
	if err != nil || first.GetId() == 0 || first.GetCreatedAt().AsTime().IsZero() {
		t.Fatalf("Create: %v, %v", first, err)
	}
	_, err = c.Create(ctx, &notespb.Draft{Title: "first"})
	wantCode(t, "Create with a taken title", err, codes.AlreadyExists)
	_, err = c.Create(ctx, &notespb.Draft{Body: "no title"})
	wantCode(t, "Create without a title", err, codes.InvalidArgument)

	got, err := c.Get(ctx, &notespb.GetRequest{Id: first.GetId()})
	if err != nil || got.GetTitle() != "first" {
		t.Fatalf("Get(%d): %v, %v", first.GetId(), got, err)
	}
	_, err = c.Get(ctx, &notespb.GetRequest{Id: 999999})
	wantCode(t, "Get of a note that is not there", err, codes.NotFound)

	imported, err := c.Import(ctx, &notespb.ImportRequest{Drafts: []*notespb.Draft{{Title: "second"}, {Title: "third", Body: "!"}}})
	if err != nil || len(imported.GetNotes()) != 2 {
		t.Fatalf("Import: %v, %v", imported, err)
	}
	_, err = c.Import(ctx, &notespb.ImportRequest{Drafts: []*notespb.Draft{{Title: "fourth"}, {Title: "first"}}})
	wantCode(t, "Import with a taken title", err, codes.AlreadyExists)
	_, err = c.Import(ctx, &notespb.ImportRequest{Drafts: []*notespb.Draft{{Title: "fifth"}, {Body: "untitled"}}})
	wantCode(t, "Import with an untitled draft", err, codes.InvalidArgument)
	_, err = c.Import(ctx, &notespb.ImportRequest{})
	wantCode(t, "Import of nothing", err, codes.InvalidArgument)

	list, err = c.List(ctx, &notespb.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, n := range list.GetNotes() {
		titles = append(titles, n.GetTitle())
	}
	if want := []string{"third", "second", "first"}; !slices.Equal(titles, want) {
		t.Fatalf("List after the imports: %q, want %q — a failed import left rows behind, or the order is not newest first", titles, want)
	}

	// The method queries with the call's context: a deadline that has
	// already passed reaches the database and comes back DeadlineExceeded.
	late := serve(t, NewServer(client, grpc.ChainUnaryInterceptor(expired)))
	_, err = late.List(ctx, &notespb.ListRequest{})
	wantCode(t, "List past its deadline", err, codes.DeadlineExceeded)
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.Check(t, "gRPC", "grpc.go", "notespb/notes.proto")
}
