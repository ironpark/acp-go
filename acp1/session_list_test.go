package acp1_test

import (
	"context"
	"slices"
	"testing"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
)

// listSession is session state that can describe itself for session/list.
type listSession struct {
	cwd      string
	updated  string
	archived bool
}

func (s listSession) SessionInfo() acp1.SessionInfo {
	updated := s.updated
	info := acp1.SessionInfo{Cwd: s.cwd, UpdatedAt: &updated}
	if s.archived {
		info.Archived = new(true)
	}
	return info
}

func seedListStore(t *testing.T, sessions map[acp1.SessionID]listSession) *acp1.MemoryStore[listSession] {
	t.Helper()
	store := acp1.NewMemoryStore[listSession]()
	for id, session := range sessions {
		if err := store.Set(t.Context(), id, session); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func newListManager(store acp1.SessionStore[listSession], opts ...acp1.SessionManagerOption) *acp1.SessionManager[listSession] {
	return acp1.NewSessionManager[listSession](store,
		func(context.Context, *acp1.NewSessionRequest) (acp1.SessionID, listSession, error) {
			return "", listSession{}, nil
		}, opts...)
}

func TestSessionManagerListPaginates(t *testing.T) {
	ctx := t.Context()
	store := seedListStore(t, map[acp1.SessionID]listSession{
		"s1": {cwd: "/", updated: "2024-03-01"},
		"s2": {cwd: "/", updated: "2024-02-01"},
		"s3": {cwd: "/", updated: "2024-01-01"},
	})
	manager := newListManager(store, acp1.WithSessionListPageSize(2))

	first, err := manager.List(ctx, &acp1.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 2 || first.Sessions[0].SessionID != "s1" || first.Sessions[1].SessionID != "s2" {
		t.Fatalf("first page = %+v", first.Sessions)
	}
	if first.NextCursor == nil {
		t.Fatal("first page should hand back a cursor")
	}

	second, err := manager.List(ctx, &acp1.ListSessionsRequest{Cursor: first.NextCursor})
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

func TestSessionManagerListNoLimitReturnsAll(t *testing.T) {
	ctx := t.Context()
	store := seedListStore(t, map[acp1.SessionID]listSession{
		"s1": {cwd: "/", updated: "2024-03-01"},
		"s2": {cwd: "/", updated: "2024-02-01"},
	})
	manager := newListManager(store, acp1.WithSessionListPageSize(0))

	response, err := manager.List(ctx, &acp1.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Sessions) != 2 || response.NextCursor != nil {
		t.Fatalf("response = %+v", response)
	}
}

func TestSessionManagerListRejectsBadCursor(t *testing.T) {
	ctx := t.Context()
	manager := newListManager(seedListStore(t, nil))
	bad := "not-a-cursor"
	if _, err := manager.List(ctx, &acp1.ListSessionsRequest{Cursor: &bad}); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Fatalf("List with a bad cursor = %v, want invalid params", err)
	}
}

// indexedStore answers session/list pages itself, the way a store backed by a
// database index would, and counts the Gets the manager makes.
type indexedStore struct {
	*acp1.MemoryStore[listSession]
	queries []acp.SessionListQuery
	gets    int
}

func (s *indexedStore) Get(ctx context.Context, id acp1.SessionID) (listSession, bool, error) {
	s.gets++
	return s.MemoryStore.Get(ctx, id)
}

func (s *indexedStore) ListSessionInfo(ctx context.Context, query acp.SessionListQuery) ([]acp1.SessionInfo, error) {
	s.queries = append(s.queries, query)
	ids, err := s.MemoryStore.List(ctx)
	if err != nil {
		return nil, err
	}
	var infos []acp1.SessionInfo
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
	slices.SortFunc(infos, func(a, b acp1.SessionInfo) int { return position(a).Compare(position(b)) })
	if query.Limit > 0 && len(infos) > query.Limit {
		infos = infos[:query.Limit]
	}
	return infos, nil
}

func position(info acp1.SessionInfo) acp.SessionListPosition {
	return acp.SessionListPosition{UpdatedAt: info.GetUpdatedAt(), SessionID: string(info.SessionID)}
}

// listAll pages through every session in cwd and returns their ids in order.
func listAll(t *testing.T, manager *acp1.SessionManager[listSession], cwd string) []acp1.SessionID {
	t.Helper()
	var ids []acp1.SessionID
	request := &acp1.ListSessionsRequest{}
	if cwd != "" {
		request.Cwd = &cwd
	}
	for range 10 {
		response, err := manager.List(t.Context(), request)
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
	sessions := map[acp1.SessionID]listSession{
		"s1": {cwd: "/a", updated: "2024-03-01"},
		"s2": {cwd: "/b", updated: "2024-02-01"},
		"s3": {cwd: "/a", updated: "2024-02-01"},
		"s4": {cwd: "/a", updated: "2024-01-01"},
		"s5": {cwd: "/a", updated: ""},
	}
	indexed := &indexedStore{MemoryStore: seedListStore(t, sessions)}
	var _ acp1.SessionInfoLister = indexed

	got := listAll(t, newListManager(indexed, acp1.WithSessionListPageSize(2)), "/a")
	want := listAll(t, newListManager(seedListStore(t, sessions), acp1.WithSessionListPageSize(2)), "/a")
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
	manager := newListManager(seedListStore(t, map[acp1.SessionID]listSession{
		"live": {cwd: "/", updated: "2024-02-01"},
		"old":  {cwd: "/", updated: "2024-01-01", archived: true},
	}))
	ids := func(filter acp1.SessionArchiveFilter) []acp1.SessionID {
		t.Helper()
		request := &acp1.ListSessionsRequest{}
		if filter != "" {
			request.Archived = &filter
		}
		page, err := manager.List(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		var out []acp1.SessionID
		for _, info := range page.Sessions {
			out = append(out, info.SessionID)
		}
		return out
	}
	for filter, want := range map[acp1.SessionArchiveFilter][]acp1.SessionID{
		"":                               {"live"},
		acp1.SessionArchiveFilterExclude: {"live"},
		acp1.SessionArchiveFilterInclude: {"live", "old"},
		acp1.SessionArchiveFilterOnly:    {"old"},
	} {
		if got := ids(filter); !slices.Equal(got, want) {
			t.Errorf("archived=%q: got %v, want %v", filter, got, want)
		}
	}
	unknown := acp1.SessionArchiveFilter("recent")
	if _, err := manager.List(ctx, &acp1.ListSessionsRequest{Archived: &unknown}); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("unknown filter: got %v, want invalid params", err)
	}
}
