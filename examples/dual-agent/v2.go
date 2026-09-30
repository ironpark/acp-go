package main

import (
	"context"
	"slices"
	"sync"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp2"
)

// v2Agent follows the v2 prompt lifecycle: the prompt response only accepts
// the user message, and the agent reports everything else, including the end
// of the turn, as session updates keyed by message id.
//
// Its embedded SessionManager serves the v2 session baseline: new, list,
// resume, close, delete and cancel. The agent keeps each session's history so
// that session/resume can replay it.
type v2Agent struct {
	*acp2.SessionManager[*v2Session]
	client acp2.Client
}

// v2Session is a session's conversation so far.
type v2Session struct {
	cwd acp2.AbsolutePath

	mu      sync.Mutex
	history []v2Exchange
	pending []v2Exchange // accepted prompts the turn has not answered yet
}

// accept adds a prompt for the turn to answer.
func (s *v2Session) accept(exchange v2Exchange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, exchange)
}

// next takes the oldest prompt not answered yet.
func (s *v2Session) next() (v2Exchange, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return v2Exchange{}, false
	}
	exchange := s.pending[0]
	s.pending = s.pending[1:]
	return exchange, true
}

// answered records an exchange in the history, for a replay.
func (s *v2Session) answered(exchange v2Exchange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, exchange)
}

// SessionInfo describes the session in session/list.
func (s *v2Session) SessionInfo() acp2.SessionInfo { return acp2.SessionInfo{Cwd: s.cwd} }

// replay returns the answered exchanges.
func (s *v2Session) replay() []v2Exchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.history)
}

// v2Exchange is one prompt and the agent's reply, with their message ids.
type v2Exchange struct {
	userMessage acp2.MessageID
	prompt      []acp2.ContentBlock
	reply       acp2.MessageID
	text        string
}

func newV2Agent(client acp2.Client) *v2Agent {
	return &v2Agent{
		SessionManager: acp2.NewSessionManager(acp2.NewMemoryStore[*v2Session](),
			func(_ context.Context, params *acp2.NewSessionRequest) (acp2.SessionID, *v2Session, error) {
				return acp2.GenerateSessionID(), &v2Session{cwd: params.Cwd}, nil
			}),
		client: client,
	}
}

func (a *v2Agent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return &acp2.InitializeResponse{
		ProtocolVersion: acp2.ProtocolVersion,
		Info:            acp2.Implementation{Name: info.name, Version: info.version},
		Capabilities:    acp2.CapabilitiesOf(a),
	}, nil
}

func (a *v2Agent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	session, err := a.Lookup(ctx, params.SessionID)
	if err != nil {
		return nil, err
	}
	userMessage := acp2.GenerateMessageID()
	stream := acp2.NewSessionStream(a.client, params.SessionID)

	// The agent must echo the user message it accepted, then report the
	// work: StartTurn reports running, runs the work after the response and
	// reports idle with the stop reason the work returns to end the turn.
	if err := stream.SendUserMessage(ctx, userMessage, params.Prompt...); err != nil {
		return nil, err
	}
	session.accept(v2Exchange{userMessage: userMessage, prompt: params.Prompt})
	// A prompt that arrives while the turn runs joins it, and the running
	// work answers it too, so the work answers every accepted prompt.
	if _, err := a.StartTurn(ctx, params.SessionID, stream, func(ctx context.Context, session *v2Session) acp2.StopReason {
		for exchange, ok := session.next(); ok; exchange, ok = session.next() {
			exchange.reply = acp2.GenerateMessageID()
			exchange.text = "v2 echo: " + acp2.JoinTexts(exchange.prompt)
			_ = stream.SendText(ctx, exchange.reply, exchange.text) // fails only once the client is gone
			session.answered(exchange)
		}
		return acp2.StopReasonEndTurn
	}); err != nil {
		return nil, err
	}
	return &acp2.PromptResponse{MessageID: userMessage}, nil
}

// ResumeSession continues a session, first replaying its history when the
// client asks to replay from the start. In v2 this replaces session/load.
func (a *v2Agent) ResumeSession(ctx context.Context, params *acp2.ResumeSessionRequest) (*acp2.ResumeSessionResponse, error) {
	response, err := a.SessionManager.ResumeSession(ctx, params) // fails for an unknown session
	if err != nil {
		return nil, err
	}
	switch params.ReplayFrom.Variant().(type) {
	case nil:
		return response, nil // continue without a replay
	case acp2.ReplayFromStart:
	default:
		return nil, acp.InvalidParams("unsupported replay cursor")
	}

	session, err := a.Lookup(ctx, params.SessionID)
	if err != nil {
		return nil, err
	}
	history := session.replay()
	// The replay goes out before the response, the same messages with the
	// same ids, so the client can rebuild the conversation.
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	for _, exchange := range history {
		if err := stream.SendUserMessage(ctx, exchange.userMessage, exchange.prompt...); err != nil {
			return nil, err
		}
		if err := stream.SendText(ctx, exchange.reply, exchange.text); err != nil {
			return nil, err
		}
	}
	return response, nil
}
