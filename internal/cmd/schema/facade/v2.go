package facade

// V2 is the method table for the draft ACP v2 façade in acp2.
//
// Which methods are required is this table's call: the upstream v2 SDK
// registers handlers per method and enforces nothing. The split keeps the
// same shape as v1 — the methods every agent or client needs to hold a
// conversation are required, everything gated by a capability is optional.
var V2 = &Spec{
	Package:       "acp2",
	CapabilityDoc: "capabilities.",
	Dir:           "acp2",
	SchemaPath:    "github.com/ironpark/acp-go/schema/v2",
	Agent: []Group{
		{
			Interface:  "Agent",
			Capability: "session",
			Required:   true,
			Doc: `Agent is the set of methods every ACP v2 agent must handle: the baseline
session methods the session capability advertises, which v2 requires
together. Embedding a [SessionManager] provides the session lifecycle ones.
Implement the optional interfaces for the rest; unimplemented methods are
answered with "method not found".`,
			Methods: []Method{
				{
					Wire: "initialize", Name: "Initialize", Params: "InitializeRequest", Response: "InitializeResponse", Via: "initialize", CallVia: "initialize",
					Doc: `Initialize negotiates the protocol version and exchanges capabilities.`,
					CallDoc: `Initialize negotiates the protocol version and exchanges capabilities. It is
the first call on every connection. A zero ProtocolVersion, or nil params, sends
[ProtocolVersion]. An agent that only speaks v1 answers
with ProtocolVersion 1 and a v1-shaped response; reconnect with the v1
package in that case.`,
				},
				{
					Wire: "session/new", Name: "NewSession", Params: "NewSessionRequest", Response: "NewSessionResponse",
					Doc:     `NewSession creates a conversation session with its own context.`,
					CallDoc: `NewSession creates a session. It may fail with an auth-required error.`,
				},
				{
					Wire: "session/list", Name: "ListSessions", Params: "ListSessionsRequest", Response: "ListSessionsResponse",
					Doc:     `ListSessions lists the agent's sessions, optionally filtered and paginated.`,
					CallDoc: `ListSessions lists sessions, optionally filtered and paginated.`,
				},
				{
					Wire: "session/resume", Name: "ResumeSession", Params: "ResumeSessionRequest", Response: "ResumeSessionResponse",
					Doc: `ResumeSession continues a session. v2 has no session/load: when the
request's ReplayFrom asks for it, the agent replays the history it retains
before answering.`,
					CallDoc: `ResumeSession continues a session, replaying its history first when
ReplayFrom asks for it.`,
				},
				{
					Wire: "session/close", Name: "CloseSession", Params: "CloseSessionRequest", Response: "CloseSessionResponse",
					Doc: `CloseSession cancels the session's ongoing work, as session/cancel does,
and frees the resources it holds on this connection.`,
					CallDoc: `CloseSession cancels any ongoing work and frees the session's resources.`,
				},
				{
					Wire: "session/prompt", Name: "Prompt", Params: "PromptRequest", Response: "PromptResponse",
					Doc: `Prompt accepts a user message into the session, starting foreground work
or contributing to the work already running, and returns once the message
is accepted. The work reports running and idle with state updates.`,
					CallDoc: `Prompt sends a user message and returns once the agent accepts it; the
work it starts ends when the agent reports idle. [ClientSession.Prompt]
follows the turn.`,
				},
				{
					Wire: "session/cancel", Name: "CancelSession", Params: "CancelSessionNotification",
					Doc: `CancelSession is a notification asking the agent to stop the session's
foreground work, which then reports idle with the cancelled stop reason.`,
					CallDoc: `CancelSession asks the agent to stop the session's foreground work.`,
				},
			},
		},
		{
			Interface:  "AuthHandler",
			Capability: "auth",
			Doc:        `AuthHandler handles auth/login and auth/logout.`,
			Methods: []Method{
				{Wire: "auth/login", Name: "Login", Params: "LoginAuthRequest", Response: "LoginAuthResponse",
					CallDoc: `Login authenticates with one of the methods the agent advertised.`},
				{Wire: "auth/logout", Name: "Logout", Params: "LogoutAuthRequest", Response: "LogoutAuthResponse",
					CallDoc: `Logout clears the credentials the agent holds.`},
			},
		},
		{
			Interface:  "SessionDeleter",
			Capability: "session.delete",
			Doc:        `SessionDeleter handles session/delete.`,
			Methods: []Method{{
				Wire: "session/delete", Name: "DeleteSession", Params: "DeleteSessionRequest", Response: "DeleteSessionResponse",
				CallDoc: `DeleteSession deletes a session and its stored history.`,
			}},
		},
		{
			Interface:    "SessionForker",
			Capability:   "session.fork",
			Experimental: true,
			Doc:          `SessionForker handles session/fork.`,
			Methods: []Method{{
				Wire: "session/fork", Name: "ForkSession", Params: "ForkSessionRequest", Response: "ForkSessionResponse",
				CallDoc: `ForkSession branches a session so work continues without touching the
original history.`,
			}},
		},
		{
			Interface:    "SessionConfigOptionSetter",
			NoCapability: true,
			Doc: `SessionConfigOptionSetter handles session/set_config_option. The response
carries every option and its current value, since changing one option may
change the others.`,
			Methods: []Method{{
				Wire: "session/set_config_option", Name: "SetSessionConfigOption", Params: "SetSessionConfigOptionRequest", Response: "SetSessionConfigOptionResponse",
				CallDoc: `SetSessionConfigOption sets one configuration option. The response returns
every option, since one change may affect the others.`,
			}},
		},
		{
			Interface:    "ProviderManager",
			Capability:   "providers",
			Experimental: true,
			Doc:          `ProviderManager handles the providers/* methods.`,
			Methods: []Method{
				{Wire: "providers/list", Name: "ListProviders", Params: "ListProvidersRequest", Response: "ListProvidersResponse",
					CallDoc: "ListProviders lists the model providers the agent can use."},
				{Wire: "providers/set", Name: "SetProvider", Params: "SetProviderRequest", Response: "SetProviderResponse",
					CallDoc: "SetProvider configures one provider."},
				{Wire: "providers/disable", Name: "DisableProvider", Params: "DisableProviderRequest", Response: "DisableProviderResponse",
					CallDoc: "DisableProvider turns one provider off."},
			},
		},
		{
			Interface:    "NesHandler",
			Capability:   "nes",
			Experimental: true,
			Doc: `NesHandler handles the nes/* methods for Next Edit Suggestions. AcceptNes
and RejectNes are notifications.`,
			Methods: nesMethods,
		},
		{
			Interface:    "DocumentHandler",
			NoCapability: true,
			Experimental: true,
			Doc: `DocumentHandler receives the document/did* notifications that mirror the
client's open editors.`,
			Methods: documentMethods,
		},
		{
			Interface:    "MCPMessageHandler",
			Capability:   "session.mcp.acp",
			Experimental: true,
			Doc: `MCPMessageHandler receives the request-scoped MCP notifications, such as
progress, that an MCP server the client provides sends over mcp/message
while it works on a request the agent made.`,
			Methods: []Method{
				{Wire: "mcp/message", Name: "NotifyMCP", Params: "MessageMCPNotification",
					CallDoc: `NotifyMCP sends the agent a notification belonging to one of its MCP requests.`},
			},
		},
	},
	Client: []Group{
		{
			Interface: "Client",
			Required:  true,
			Doc: `Client is the set of methods every ACP v2 client must handle. MCP and
elicitation support are optional; implement the matching interface and
advertise the capability from ` + "`InitializeRequest.Capabilities`" + `.`,
			Methods: []Method{
				{
					Wire: "session/update", Name: "SessionUpdate", Params: "UpdateSessionNotification", Via: "sessionUpdate",
					Doc: `SessionUpdate is a notification streaming turn progress to the user.

Notifications are handled one at a time, in order, off the loop that reads
the connection, so a slow handler holds back neither the agent's messages
nor $/cancel_request. Updates stay ahead of the prompt response, and a
request from the agent is handled only after the updates sent before it.
A handler may call the agent and wait for the answer, unless answering needs
a request back to the client: that request waits for the handler to return.
Hand such calls to a goroutine.`,
					CallDoc: `SessionUpdate streams turn progress to the client.`,
				},
				{
					Wire: "session/request_permission", Name: "RequestPermission", Params: "RequestPermissionRequest", Response: "RequestPermissionResponse", Untimed: true,
					Doc: `RequestPermission asks the user to authorize a tool call. When the turn
is cancelled the client MUST answer with the cancelled outcome rather
than leaving the request pending.`,
					CallDoc: `RequestPermission asks the user to authorize a tool call.`,
				},
			},
		},
		{
			Interface:    "MCPProvider",
			NoCapability: true,
			Experimental: true,
			Doc: `MCPProvider serves the MCP servers the client lists with the "acp"
transport in session setup. Each mcp/message request is one MCP operation for
the server its serverId names, identified by its own requestId; there is no
MCP connection or initialization handshake. In v2 this replaces the v1 fs/*
and terminal/* methods.`,
			Methods: []Method{
				{Wire: "mcp/message", Name: "MessageMCP", Params: "MessageMCPRequest", Response: "MessageMCPResponse",
					CallDoc: `MessageMCP sends one MCP request to a server the client provides and returns
its outcome: the MCP result, or the MCP error, which is not an ACP error.`},
			},
		},
		{
			Interface:  "ElicitationHandler",
			Capability: "elicitation",
			Doc: `ElicitationHandler handles elicitation/create and the elicitation/complete
notification.`,
			Methods: elicitationMethods("capabilities.elicitation"),
		},
	},
}
