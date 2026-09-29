package main

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/ironpark/acp-go/acp1"
)

// titleLength is how many characters of the first prompt a session's title
// keeps.
const titleLength = 60

// sessionDir is where sessions are saved: $OPEN_AGENT_SESSIONS, or
// open-agent/sessions in the user's cache directory.
func sessionDir() (string, error) {
	if dir := os.Getenv("OPEN_AGENT_SESSIONS"); dir != "" {
		return dir, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "open-agent", "sessions"), nil
}

// savedSession is a session as its file holds it.
type savedSession struct {
	Cwd     string             `json:"cwd"`
	Mode    acp1.SessionModeID `json:"mode"`
	History []message          `json:"history,omitzero"`
	Cost    float64            `json:"cost,omitzero"`
	Updated time.Time          `json:"updated,omitzero"`
}

// MarshalJSON saves the session for the store; its fields are unexported, and
// a turn may be changing them.
func (s *session) MarshalJSON() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Marshal(savedSession{Cwd: s.cwd, Mode: s.mode, History: s.history, Cost: s.cost, Updated: s.updated})
}

// UnmarshalJSON restores a session the store saved.
func (s *session) UnmarshalJSON(data []byte) error {
	var saved savedSession
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	s.cwd, s.mode, s.history, s.cost, s.updated = saved.Cwd, saved.Mode, saved.History, saved.Cost, saved.Updated
	return nil
}

// SessionInfo describes the session for session/list: its directory, when
// its conversation last changed, and its first prompt as the title.
func (s *session) SessionInfo() acp1.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := acp1.SessionInfo{Cwd: s.cwd}
	if !s.updated.IsZero() {
		info.UpdatedAt = new(s.updated.UTC().Format(time.RFC3339Nano))
	}
	for _, m := range s.history {
		if m.Role == "user" {
			title := []rune(m.Content)
			info.Title = new(string(title[:min(len(title), titleLength)]))
			break
		}
	}
	return info
}

// ListSessions lists the saved sessions, newest first, through the embedded
// manager.
func (a *openAgent) ListSessions(ctx context.Context, params *acp1.ListSessionsRequest) (*acp1.ListSessionsResponse, error) {
	return a.List(ctx, params)
}

// save writes a session changed outside a turn to the store; the manager
// saves the changes turns make. A failure is logged rather than failing the
// request that changed the session.
func (a *openAgent) save(ctx context.Context, id acp1.SessionID, sess *session) {
	if err := a.Save(ctx, id, sess); err != nil {
		a.logger.Error("save session", "session", id, "error", err)
	}
}
