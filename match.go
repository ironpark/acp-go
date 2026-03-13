package acp

import "fmt"

// SessionUpdateMatcher defines handlers for each SessionUpdate variant.
//
// Use with MatchSessionUpdate for exhaustive pattern matching on SessionUpdate values.
// If Default is nil, unhandled variants will panic. Set Default to handle unknown variants gracefully.
type SessionUpdateMatcher[T any] struct {
	UserMessageChunk        func(SessionUpdateUserMessageChunk) T
	AgentMessageChunk       func(SessionUpdateAgentMessageChunk) T
	AgentThoughtChunk       func(SessionUpdateAgentThoughtChunk) T
	ToolCall                func(SessionUpdateToolCall) T
	ToolCallUpdate          func(SessionUpdateToolCallUpdate) T
	Plan                    func(SessionUpdatePlan) T
	AvailableCommandsUpdate func(SessionUpdateAvailableCommandsUpdate) T
	CurrentModeUpdate       func(SessionUpdateCurrentModeUpdate) T
	ConfigOptionUpdate      func(SessionUpdateConfigOptionUpdate) T
	SessionInfoUpdate       func(SessionUpdateSessionInfoUpdate) T
	Default                 func() T
}

// MatchSessionUpdate applies exhaustive pattern matching on a SessionUpdate.
//
// Calls the handler matching the active variant. If the handler is nil,
// falls back to Default. If Default is also nil, panics.
func MatchSessionUpdate[T any](u *SessionUpdate, m SessionUpdateMatcher[T]) T {
	if v, ok := u.AsUserMessageChunk(); ok {
		if m.UserMessageChunk != nil {
			return m.UserMessageChunk(v)
		}
		return matchDefault(m.Default, "UserMessageChunk")
	}
	if v, ok := u.AsAgentMessageChunk(); ok {
		if m.AgentMessageChunk != nil {
			return m.AgentMessageChunk(v)
		}
		return matchDefault(m.Default, "AgentMessageChunk")
	}
	if v, ok := u.AsAgentThoughtChunk(); ok {
		if m.AgentThoughtChunk != nil {
			return m.AgentThoughtChunk(v)
		}
		return matchDefault(m.Default, "AgentThoughtChunk")
	}
	if v, ok := u.AsToolCall(); ok {
		if m.ToolCall != nil {
			return m.ToolCall(v)
		}
		return matchDefault(m.Default, "ToolCall")
	}
	if v, ok := u.AsToolCallUpdate(); ok {
		if m.ToolCallUpdate != nil {
			return m.ToolCallUpdate(v)
		}
		return matchDefault(m.Default, "ToolCallUpdate")
	}
	if v, ok := u.AsPlan(); ok {
		if m.Plan != nil {
			return m.Plan(v)
		}
		return matchDefault(m.Default, "Plan")
	}
	if v, ok := u.AsAvailableCommandsUpdate(); ok {
		if m.AvailableCommandsUpdate != nil {
			return m.AvailableCommandsUpdate(v)
		}
		return matchDefault(m.Default, "AvailableCommandsUpdate")
	}
	if v, ok := u.AsCurrentModeUpdate(); ok {
		if m.CurrentModeUpdate != nil {
			return m.CurrentModeUpdate(v)
		}
		return matchDefault(m.Default, "CurrentModeUpdate")
	}
	if v, ok := u.AsConfigOptionUpdate(); ok {
		if m.ConfigOptionUpdate != nil {
			return m.ConfigOptionUpdate(v)
		}
		return matchDefault(m.Default, "ConfigOptionUpdate")
	}
	if v, ok := u.AsSessionInfoUpdate(); ok {
		if m.SessionInfoUpdate != nil {
			return m.SessionInfoUpdate(v)
		}
		return matchDefault(m.Default, "SessionInfoUpdate")
	}
	panic("SessionUpdate has no variant set")
}

// ContentBlockMatcher defines handlers for each ContentBlock variant.
type ContentBlockMatcher[T any] struct {
	Text         func(ContentBlockText) T
	Image        func(ContentBlockImage) T
	Audio        func(ContentBlockAudio) T
	ResourceLink func(ContentBlockResourceLink) T
	Resource     func(ContentBlockResource) T
	Default      func() T
}

// MatchContentBlock applies exhaustive pattern matching on a ContentBlock.
func MatchContentBlock[T any](c *ContentBlock, m ContentBlockMatcher[T]) T {
	if v, ok := c.AsText(); ok {
		if m.Text != nil {
			return m.Text(v)
		}
		return matchDefault(m.Default, "Text")
	}
	if v, ok := c.AsImage(); ok {
		if m.Image != nil {
			return m.Image(v)
		}
		return matchDefault(m.Default, "Image")
	}
	if v, ok := c.AsAudio(); ok {
		if m.Audio != nil {
			return m.Audio(v)
		}
		return matchDefault(m.Default, "Audio")
	}
	if v, ok := c.AsResourceLink(); ok {
		if m.ResourceLink != nil {
			return m.ResourceLink(v)
		}
		return matchDefault(m.Default, "ResourceLink")
	}
	if v, ok := c.AsResource(); ok {
		if m.Resource != nil {
			return m.Resource(v)
		}
		return matchDefault(m.Default, "Resource")
	}
	panic("ContentBlock has no variant set")
}

// ToolCallContentMatcher defines handlers for each ToolCallContent variant.
type ToolCallContentMatcher[T any] struct {
	Content  func(ToolCallContentContent) T
	Diff     func(ToolCallContentDiff) T
	Terminal func(ToolCallContentTerminal) T
	Default  func() T
}

// MatchToolCallContent applies exhaustive pattern matching on a ToolCallContent.
func MatchToolCallContent[T any](t *ToolCallContent, m ToolCallContentMatcher[T]) T {
	if v, ok := t.AsContent(); ok {
		if m.Content != nil {
			return m.Content(v)
		}
		return matchDefault(m.Default, "Content")
	}
	if v, ok := t.AsDiff(); ok {
		if m.Diff != nil {
			return m.Diff(v)
		}
		return matchDefault(m.Default, "Diff")
	}
	if v, ok := t.AsTerminal(); ok {
		if m.Terminal != nil {
			return m.Terminal(v)
		}
		return matchDefault(m.Default, "Terminal")
	}
	panic("ToolCallContent has no variant set")
}

// RequestPermissionOutcomeMatcher defines handlers for each RequestPermissionOutcome variant.
type RequestPermissionOutcomeMatcher[T any] struct {
	Selected  func(RequestPermissionOutcomeSelected) T
	Cancelled func(RequestPermissionOutcomeCancelled) T
	Default   func() T
}

// MatchRequestPermissionOutcome applies exhaustive pattern matching on a RequestPermissionOutcome.
func MatchRequestPermissionOutcome[T any](o *RequestPermissionOutcome, m RequestPermissionOutcomeMatcher[T]) T {
	if v, ok := o.AsSelected(); ok {
		if m.Selected != nil {
			return m.Selected(v)
		}
		return matchDefault(m.Default, "Selected")
	}
	if v, ok := o.AsCancelled(); ok {
		if m.Cancelled != nil {
			return m.Cancelled(v)
		}
		return matchDefault(m.Default, "Cancelled")
	}
	panic("RequestPermissionOutcome has no variant set")
}

// MCPServerMatcher defines handlers for each MCPServer variant.
type MCPServerMatcher[T any] struct {
	HTTP    func(MCPServerHTTP) T
	SSE     func(MCPServerSSE) T
	Stdio   func(MCPServerStdio) T
	Default func() T
}

// MatchMCPServer applies exhaustive pattern matching on an MCPServer.
func MatchMCPServer[T any](s *MCPServer, m MCPServerMatcher[T]) T {
	if v, ok := s.AsHTTP(); ok {
		if m.HTTP != nil {
			return m.HTTP(v)
		}
		return matchDefault(m.Default, "HTTP")
	}
	if v, ok := s.AsSSE(); ok {
		if m.SSE != nil {
			return m.SSE(v)
		}
		return matchDefault(m.Default, "SSE")
	}
	if v, ok := s.AsStdio(); ok {
		if m.Stdio != nil {
			return m.Stdio(v)
		}
		return matchDefault(m.Default, "Stdio")
	}
	panic("MCPServer has no variant set")
}

func matchDefault[T any](fn func() T, variant string) T {
	if fn != nil {
		return fn()
	}
	panic(fmt.Sprintf("unhandled variant %s and no Default handler set", variant))
}
