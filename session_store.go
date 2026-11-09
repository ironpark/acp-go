package acp

import (
	"context"
	"sync"
	"uuid"

	"github.com/ironpark/acp-go/internal/acpconn"
)

// SessionStore keeps per-session state for an agent, keyed by the protocol
// version's session id type. Implementations must be safe for concurrent use.
//
// Every method takes the context of the request it serves and can fail, so a
// store backed by a database or a remote cache can report its errors; the
// session manager returns them to the client.
type SessionStore[ID comparable, T any] interface {
	// Get returns the session with the given id, or false if there is none.
	Get(ctx context.Context, id ID) (T, bool, error)
	// Set stores a session, replacing any session with the same id.
	Set(ctx context.Context, id ID, session T) error
	// Delete removes a session. Deleting an unknown id is not an error.
	Delete(ctx context.Context, id ID) error
	// List returns the stored session ids.
	List(ctx context.Context) ([]ID, error)
}

// MemoryStore keeps sessions in memory for the life of the process. The zero
// value is an empty store ready to use.
type MemoryStore[ID comparable, T any] struct {
	mu       sync.RWMutex
	sessions map[ID]T
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore[ID comparable, T any]() *MemoryStore[ID, T] {
	return &MemoryStore[ID, T]{sessions: make(map[ID]T)}
}

// Get returns the session stored under id and whether there is one.
func (s *MemoryStore[ID, T]) Get(_ context.Context, id ID) (T, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	return session, ok, nil
}

// Set stores session under id, replacing any session stored there.
func (s *MemoryStore[ID, T]) Set(_ context.Context, id ID, session T) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[ID]T)
	}
	s.sessions[id] = session
	return nil
}

// Delete removes the session stored under id, if any.
func (s *MemoryStore[ID, T]) Delete(_ context.Context, id ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

// List returns the ids of the stored sessions, in no particular order.
func (s *MemoryStore[ID, T]) List(context.Context) ([]ID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]ID, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	return ids, nil
}

// SessionListPosition is a session's place in a session/list result, which
// lists the most recently updated sessions first and breaks ties by session
// id ascending.
type SessionListPosition struct {
	// UpdatedAt is the session's last-activity timestamp, compared as a
	// string, so a store must write every timestamp in one format, such as
	// RFC 3339 in UTC. A session without one sorts after every dated session.
	UpdatedAt string
	// SessionID breaks ties between sessions with the same UpdatedAt.
	SessionID string
}

// Compare orders two positions the way session/list does. A negative result
// means p is listed before other, a positive one after it.
func (p SessionListPosition) Compare(other SessionListPosition) int {
	return acpconn.SessionPosition(p).Compare(acpconn.SessionPosition(other))
}

// SessionListQuery selects one page of sessions for a [SessionInfoLister].
type SessionListQuery struct {
	// Cwd, when not empty, keeps only the sessions in this working directory.
	Cwd string
	// Archived selects sessions by archive state, as session/list's archived
	// filter does: "" or "exclude" keeps the sessions not archived,
	// "include" keeps every session and "only" the archived ones. The manager
	// rejects any other value before it asks. A store whose sessions are never
	// archived may ignore it.
	Archived string
	// After, when not nil, keeps only the sessions listed after this
	// position; a nil After starts at the most recently updated session.
	After *SessionListPosition
	// Limit, when positive, is the most sessions to return; zero or less
	// returns every match.
	Limit int
}

// SessionInfoLister is implemented by a [SessionStore] that can answer
// session/list itself, such as one backed by a database index. The session
// manager of each façade uses it instead of reading and describing every
// stored session for each page, so a page costs the store one query.
//
// ListSessionInfo returns the sessions that match query, in the order
// [SessionListPosition.Compare] defines, with each SessionID set. The manager
// asks for one more session than a page holds and returns the page and next
// cursor from the result, so a store that stops short of Limit ends the
// listing.
type SessionInfoLister[Info any] interface {
	ListSessionInfo(ctx context.Context, query SessionListQuery) ([]Info, error)
}

// GenerateSessionID returns a new id of the form "session_<UUIDv7>".
func GenerateSessionID() string { return GenerateID("session") }

// GenerateID returns a new id of the form "<prefix>_<UUIDv7>", for the ids an
// agent mints: sessions, messages, tool calls. A version 7 UUID starts with
// its creation time, so ids sort in the order they were minted, which keeps
// the index of a database-backed store compact.
func GenerateID(prefix string) string {
	return prefix + "_" + uuid.NewV7().String()
}
