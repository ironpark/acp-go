package acp2_test

import (
	"context"
	"slices"
	"testing"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp2"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// listSession is session state that can describe itself for session/list.
type listSession struct {
	cwd      string
	updated  string
	archived bool
}

func (s listSession) SessionInfo() acp2.SessionInfo {
	updated := s.updated
	info := acp2.SessionInfo{Cwd: acp2.AbsolutePath(s.cwd), UpdatedAt: &updated}
	if s.archived {
		info.Archived = new(true)
	}
	return info
}

func seedListStore(t *testing.T, sessions map[acp2.SessionID]listSession) *acp2.MemoryStore[listSession] {
	t.Helper()
	store := acp2.NewMemoryStore[listSession]()
	for id, session := range sessions {
		if err := store.Set(t.Context(), id, session); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func newListManager(store acp2.SessionStore[listSession], opts ...acp2.SessionManagerOption) *acp2.SessionManager[listSession] {
	return acp2.NewSessionManager[listSession](store,
		func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, listSession, error) {
			return "", listSession{}, nil
		}, opts...)
}

func TestSessionManagerListPaginates(t *testing.T) {
	ctx := t.Context()
	store := seedListStore(t, map[acp2.SessionID]listSession{
		"s1": {cwd: "/", updated: "2024-03-01"},
		"s2": {cwd: "/", updated: "2024-02-01"},
		"s3": {cwd: "/", updated: "2024-01-01"},
	})
	manager := newListManager(store, acp2.WithSessionListPageSize(2))

	first, err := manager.ListSessions(ctx, &acp2.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 2 || first.Sessions[0].SessionID != "s1" || first.Sessions[1].SessionID != "s2" {
		t.Fatalf("first page = %+v", first.Sessions)
	}
	if first.NextCursor == nil {
		t.Fatal("first page should hand back a cursor")
	}

	second, err := manager.ListSessions(ctx, &acp2.ListSessionsRequest{Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].SessionID != "s3" {
		t.Fatalf("second page = %+v", second.Sessions)
	}
	if second.NextCursor != nil {
		t.Fatalf("last page should not hand back a cursor, got %q", *second.NextCursor)
	}
}

func TestSessionManagerListRejectsBadCursor(t *testing.T) {
	ctx := t.Context()
	manager := newListManager(seedListStore(t, nil))
	bad := schema.SessionListCursor("not-a-cursor")
	if _, err := manager.ListSessions(ctx, &acp2.ListSessionsRequest{Cursor: &bad}); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Fatalf("ListSessions with a bad cursor = %v, want invalid params", err)
	}
}

// indexedStore answers session/list pages itself, the way a store backed by a
// database index would, and counts the Gets the manager makes.
type indexedStore struct {
	*acp2.MemoryStore[listSession]
	queries []acp.SessionListQuery
	gets    int
}

func (s *indexedStore) Get(ctx context.Context, id acp2.SessionID) (listSession, bool, error) {
	s.gets++
	return s.MemoryStore.Get(ctx, id)
}

func (s *indexedStore) ListSessionInfo(ctx context.Context, query acp.SessionListQuery) ([]acp2.SessionInfo, error) {
	s.queries = append(s.queries, query)
	ids, err := s.MemoryStore.List(ctx)
	if err != nil {
		return nil, err
	}
	var infos []acp2.SessionInfo
	for _, id := range ids {
		session, _, _ := s.MemoryStore.Get(ctx, id)
		info := session.SessionInfo()
		info.SessionID = id
		if query.Cwd != "" && string(info.Cwd) != query.Cwd {
			continue
		}
		if query.After != nil && position(info).Compare(*query.After) <= 0 {
			continue
		}
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b acp2.SessionInfo) int { return position(a).Compare(position(b)) })
	if query.Limit > 0 && len(infos) > query.Limit {
		infos = infos[:query.Limit]
	}
	return infos, nil
}

func position(info acp2.SessionInfo) acp.SessionListPosition {
	return acp.SessionListPosition{UpdatedAt: info.GetUpdatedAt(), SessionID: string(info.SessionID)}
}

// listAll pages through every session in cwd and returns their ids in order.
func listAll(t *testing.T, manager *acp2.SessionManager[listSession], cwd string) []acp2.SessionID {
	t.Helper()
	var ids []acp2.SessionID
	request := &acp2.ListSessionsRequest{}
	if cwd != "" {
		request.Cwd = new(acp2.AbsolutePath(cwd))
	}
	for range 10 {
		response, err := manager.ListSessions(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, info := range response.Sessions {
			ids = append(ids, info.SessionID)
		}
		if response.NextCursor == nil {
			return ids
		}
		request.Cursor = response.NextCursor
	}
	t.Fatal("listing did not end")
	return nil
}

func TestSessionManagerListUsesSessionInfoLister(t *testing.T) {
	sessions := map[acp2.SessionID]listSession{
		"s1": {cwd: "/a", updated: "2024-03-01"},
		"s2": {cwd: "/b", updated: "2024-02-01"},
		"s3": {cwd: "/a", updated: "2024-02-01"},
		"s4": {cwd: "/a", updated: "2024-01-01"},
		"s5": {cwd: "/a", updated: ""},
	}
	indexed := &indexedStore{MemoryStore: seedListStore(t, sessions)}
	var _ acp2.SessionInfoLister = indexed

	got := listAll(t, newListManager(indexed, acp2.WithSessionListPageSize(2)), "/a")
	want := listAll(t, newListManager(seedListStore(t, sessions), acp2.WithSessionListPageSize(2)), "/a")
	if !slices.Equal(got, want) || len(want) != 4 {
		t.Fatalf("indexed listing = %v, scanning listing = %v", got, want)
	}
	if indexed.gets != 0 {
		t.Fatalf("manager made %d Gets on a SessionInfoLister", indexed.gets)
	}
	if len(indexed.queries) != 2 {
		t.Fatalf("queries = %+v, want one per page", indexed.queries)
	}
	for i, query := range indexed.queries {
		if query.Cwd != "/a" || query.Limit != 3 || (query.After == nil) != (i == 0) {
			t.Errorf("query %d = %+v", i, query)
		}
	}
}

func TestSessionManagerListArchivedFilter(t *testing.T) {
	ctx := t.Context()
	manager := newListManager(seedListStore(t, map[acp2.SessionID]listSession{
		"live": {cwd: "/", updated: "2024-02-01"},
		"old":  {cwd: "/", updated: "2024-01-01", archived: true},
	}))
	ids := func(filter acp2.SessionArchiveFilter) []acp2.SessionID {
		t.Helper()
		request := &acp2.ListSessionsRequest{}
		if filter != "" {
			request.Archived = &filter
		}
		page, err := manager.ListSessions(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		var out []acp2.SessionID
		for _, info := range page.Sessions {
			out = append(out, info.SessionID)
		}
		return out
	}
	for filter, want := range map[acp2.SessionArchiveFilter][]acp2.SessionID{
		"":                               {"live"},
		acp2.SessionArchiveFilterExclude: {"live"},
		acp2.SessionArchiveFilterInclude: {"live", "old"},
		acp2.SessionArchiveFilterOnly:    {"old"},
	} {
		if got := ids(filter); !slices.Equal(got, want) {
			t.Errorf("archived=%q: got %v, want %v", filter, got, want)
		}
	}
	unknown := acp2.SessionArchiveFilter("recent")
	if _, err := manager.ListSessions(ctx, &acp2.ListSessionsRequest{Archived: &unknown}); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("unknown filter: got %v, want invalid params", err)
	}
}
