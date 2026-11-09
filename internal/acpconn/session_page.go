package acpconn

import (
	"container/heap"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// SessionPosition is a session's place in a session/list result: the sort key
// both façades' session managers order by. Clients see it only as the opaque
// cursor token [SessionPager] hands back.
type SessionPosition struct {
	// UpdatedAt is the session's last-activity timestamp, the primary sort
	// key. A session without one sorts after every dated session.
	UpdatedAt string `json:"u,omitzero"`
	// SessionID breaks ties between sessions with the same UpdatedAt.
	SessionID string `json:"i"`
}

// Compare orders two positions the way session/list orders sessions: the
// newest UpdatedAt first, then session id ascending. A positive result means p
// sorts after other, so other is on an earlier page.
func (p SessionPosition) Compare(other SessionPosition) int {
	if p.UpdatedAt != other.UpdatedAt {
		return strings.Compare(other.UpdatedAt, p.UpdatedAt)
	}
	return strings.Compare(p.SessionID, other.SessionID)
}

// encodeSessionCursor encodes a position as the opaque token a session/list
// request or response carries.
func encodeSessionCursor(p SessionPosition) string {
	data, err := json.Marshal(p)
	if err != nil {
		// A position is two strings; encoding cannot fail.
		panic("acpconn: encode session list cursor: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeSessionCursor decodes a token from a session/list request. A malformed
// token becomes an invalid-params error, ready to return to the client.
func decodeSessionCursor(token string) (SessionPosition, error) {
	var p SessionPosition
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return p, jsonrpc.InvalidParams("invalid cursor")
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, jsonrpc.InvalidParams("invalid cursor")
	}
	return p, nil
}

// SessionPager collects one page of a session/list result while the caller
// streams every session through [SessionPager.Add], so a page costs memory for
// the page alone and never sorts the whole result.
//
// Sessions are ordered by their key position, newest UpdatedAt first and then
// session id ascending. The page skips every session at or before the request
// cursor and takes up to size sessions; a size of zero or less takes every
// remaining session.
type SessionPager[T any] struct {
	key   func(T) SessionPosition
	after *SessionPosition
	size  int
	// kept holds the best candidates, up to size+1 of them, as a heap with
	// the one that sorts last on top, so the next better session evicts it.
	kept sessionHeap[T]
}

// pagedSession is a candidate with its position, computed once in Add.
type pagedSession[T any] struct {
	pos  SessionPosition
	item T
}

// sessionHeap is a [heap.Interface] with the candidate that sorts last on top.
type sessionHeap[T any] []pagedSession[T]

func (h sessionHeap[T]) Len() int           { return len(h) }
func (h sessionHeap[T]) Less(i, j int) bool { return h[i].pos.Compare(h[j].pos) > 0 }
func (h sessionHeap[T]) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *sessionHeap[T]) Push(x any)        { *h = append(*h, x.(pagedSession[T])) }
func (h *sessionHeap[T]) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// NewSessionPager starts a page after the request's cursor token, or at the
// first session when cursor is "". A malformed cursor is returned as an
// invalid-params error before the caller reads any session.
func NewSessionPager[T any](cursor string, size int, key func(T) SessionPosition) (*SessionPager[T], error) {
	p := &SessionPager[T]{key: key, size: size}
	if cursor != "" {
		after, err := decodeSessionCursor(cursor)
		if err != nil {
			return nil, err
		}
		p.after = &after
	}
	return p, nil
}

// After is the position the page starts after, or nil for the first page.
func (p *SessionPager[T]) After() *SessionPosition {
	if p.after == nil {
		return nil
	}
	after := *p.after
	return &after
}

// Limit is how many sessions, in order after [SessionPager.After], the pager
// needs to fill the page and tell whether another follows: one more than the
// page size, or zero when the page takes every session.
func (p *SessionPager[T]) Limit() int {
	if p.size <= 0 {
		return 0
	}
	return p.size + 1
}

// Add offers one session for the page.
func (p *SessionPager[T]) Add(item T) {
	candidate := pagedSession[T]{pos: p.key(item), item: item}
	if p.after != nil && candidate.pos.Compare(*p.after) <= 0 {
		return // on an earlier page
	}
	switch {
	case p.size <= 0:
		p.kept = append(p.kept, candidate)
	case len(p.kept) <= p.size:
		// One extra candidate tells Page whether another page follows.
		heap.Push(&p.kept, candidate)
	case candidate.pos.Compare(p.kept[0].pos) < 0:
		p.kept[0] = candidate
		heap.Fix(&p.kept, 0)
	}
}

// Page returns the page in session/list order and the token to pass as the
// request cursor for the following page, or "" when the page is the last.
func (p *SessionPager[T]) Page() (page []T, next string) {
	slices.SortFunc(p.kept, func(a, b pagedSession[T]) int { return a.pos.Compare(b.pos) })
	kept := p.kept
	if p.size > 0 && len(kept) > p.size {
		kept = kept[:p.size]
		next = encodeSessionCursor(kept[len(kept)-1].pos)
	}
	page = make([]T, len(kept))
	for i, candidate := range kept {
		page[i] = candidate.item
	}
	return page, next
}

// ArchiveFilter returns which archive states session/list's archived filter
// keeps: "" and "exclude" the sessions not archived, "include" every session,
// "only" the archived ones. Any other value is an invalid-params error, which
// the protocol asks agents to return rather than read it as exclude.
func ArchiveFilter[F ~string](filter F) (func(archived bool) bool, error) {
	switch filter {
	case "", "exclude":
		return func(archived bool) bool { return !archived }, nil
	case "include":
		return func(bool) bool { return true }, nil
	case "only":
		return func(archived bool) bool { return archived }, nil
	}
	return nil, jsonrpc.InvalidParams(fmt.Sprintf("unsupported archived filter %q", string(filter)))
}
