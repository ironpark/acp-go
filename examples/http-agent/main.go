// Command http-agent serves the echo agent over Streamable HTTP instead of
// stdio, at http://localhost:8000/acp.
//
//	go run ./examples/http-agent  # listens on localhost:8000
//	go run ./examples/http-client # in another terminal
//
// With -token, it accepts only clients that send that bearer token, as
// http-client -token does.
//
// acphttp.Server implements the remote transport the other ACP SDKs use, in
// both its profiles on one endpoint: Streamable HTTP, where the client POSTs
// messages and reads the agent's from Server-Sent Events streams, and
// WebSocket. Each client gets its own connection and its own agent; the agent
// code is the same as over stdio. Sessions are shared by all connections, so a
// client that reconnects can load its session: see http-client -reconnect.
package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acphttp"
)

// history is what a session said. Sessions outlive connections, so a client
// that reconnects can load one and see it again.
type history struct {
	mu    sync.Mutex
	turns []turn
}

type turn struct{ prompt, reply string }

// echoAgent is the echo example's agent, replying with an "echo: " prefix.
// The embedded SessionManager creates and loads sessions; it is shared by
// every connection.
type echoAgent struct {
	*acp1.SessionManager[*history]
	client acp1.Client
}

func (a *echoAgent) Initialize(_ context.Context, _ *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	// CapabilitiesOf advertises loadSession, since the agent can load.
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion, AgentCapabilities: acp1.CapabilitiesOf(a)}, nil
}

func (a *echoAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return a.RunTurn(ctx, params.SessionID, func(ctx context.Context, h *history) (acp1.StopReason, error) {
		stream := acp1.NewSessionStream(a.client, params.SessionID)
		for text := range acp1.Texts(params.Prompt) {
			reply := "echo: " + text
			if err := stream.SendText(ctx, reply); err != nil {
				return "", err
			}
			h.mu.Lock()
			h.turns = append(h.turns, turn{text, reply})
			h.mu.Unlock()
		}
		return acp1.StopReasonEndTurn, nil
	})
}

// LoadSession replays the session's history before answering, as the
// protocol asks.
func (a *echoAgent) LoadSession(ctx context.Context, params *acp1.LoadSessionRequest) (*acp1.LoadSessionResponse, error) {
	h, err := a.Lookup(ctx, params.SessionID)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	turns := slices.Clone(h.turns)
	h.mu.Unlock()
	stream := acp1.NewSessionStream(a.client, params.SessionID)
	for _, t := range turns {
		if err := stream.SendUserMessage(ctx, t.prompt); err != nil {
			return nil, err
		}
		if err := stream.SendText(ctx, t.reply); err != nil {
			return nil, err
		}
	}
	return &acp1.LoadSessionResponse{}, nil
}

func main() {
	addr := flag.String("addr", "localhost:8000", "address to listen on")
	token := flag.String("token", "", "require this bearer token from clients")
	flag.Parse()

	sessions := acp1.NewSessionManager(acp1.NewMemoryStore[*history](),
		func(context.Context, *acp1.NewSessionRequest) (acp1.SessionID, *history, error) {
			return acp1.GenerateSessionID(), &history{}, nil
		})
	// serve runs once per connection, with that connection's transport.
	server := acphttp.NewServer(func(ctx context.Context, t acp.Transport) error {
		conn := acp1.NewAgentSideConnection(func(c *acp1.AgentSideConnection) acp1.Agent {
			return &echoAgent{SessionManager: sessions, client: c}
		}, t)
		return conn.Start(ctx)
	}, acphttp.WithErrorHandler(func(err error) { log.Printf("connection: %v", err) }))

	mux := http.NewServeMux()
	mux.Handle("/acp", requireToken(*token, server))
	// The protocol asks for HTTP/2; plain-text HTTP/2 needs it enabled, and
	// HTTP/1.1 keeps working for clients without it.
	httpServer := &http.Server{Addr: *addr, Handler: mux, Protocols: new(http.Protocols)}
	httpServer.Protocols.SetHTTP1(true)
	httpServer.Protocols.SetUnencryptedHTTP2(true)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failed := make(chan error, 1)
	go func() { failed <- httpServer.ListenAndServe() }()
	log.Printf("serving an ACP agent on http://%s/acp", *addr)
	select {
	case err := <-failed:
		log.Fatal(err)
	case <-ctx.Done():
	}

	// On Ctrl-C, stop taking new connections and give those in progress a
	// few seconds to end. The HTTP server keeps running meanwhile, since
	// their clients still POST and stream over it; any left at the deadline
	// are ended. Then the HTTP server has no long-lived requests to wait for.
	log.Print("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		log.Printf("ended the connections still open: %v", err)
	}
	shutdownHTTP, cancelHTTP := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHTTP()
	if err := httpServer.Shutdown(shutdownHTTP); err != nil {
		log.Print(err)
	}
}

// requireToken rejects requests without "Authorization: Bearer <token>", or
// lets every request through when token is empty. Server is an ordinary
// http.Handler, so authentication is ordinary middleware in front of it; it
// covers POSTs, event streams and WebSocket upgrades alike.
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
