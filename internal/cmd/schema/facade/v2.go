package facade

// V2 is the method table for the draft ACP v2 façade in acp2.
//
// Which methods are required is this table's call: the upstream v2 SDK
// registers handlers per method and enforces nothing. The split keeps the
// same shape as v1 — the methods every agent or client needs to hold a
// conversation are required, everything gated by a capability is optional.
var V2 = &Spec{
	Package:    "acp2",
	Dir:        "acp2",
	SchemaPath: "github.com/ironpark/acp-go/schema/v2",
	Agent: []Group{
		{
			Interface: "Agent",
			Required:  true,
			Doc: `Agent is the set of methods every ACP v2 agent must handle. Implement the
optional interfaces for the rest; unimplemented methods are answered with
"method not found".`,
			Methods: []Method{
				{
					Wire: "initialize", Name: "Initialize", Params: "InitializeRequest", Response: "InitializeResponse", CallVia: "initialize",
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
					Wire: "session/prompt", Name: "Prompt", Params: "PromptRequest", Response: "PromptResponse",
					Doc: `Prompt runs one prompt turn and returns once it stops.`,
					CallDoc: `Prompt runs one prompt turn and returns once the agent stops. Cancelling
ctx cancels the JSON-RPC request; to cancel the turn itself with the
protocol's own semantics, send [ClientSideConnection.CancelSession].`,
				},
				{
					Wire: "session/cancel", Name: "CancelSession", Params: "CancelSessionNotification",
					Doc: `CancelSession is a notification asking the agent to abort the current
turn. The pending Prompt call should return with the cancelled outcome.`,
					CallDoc: `CancelSession asks the agent to end the current turn.`,
				},
			},
		},
		{
			Interface: "AuthHandler",
			Doc: `AuthHandler handles auth/login and auth/logout. Advertise it with the
` + "`capabilities.auth`" + ` agent capability.`,
			Methods: []Method{
				{Wire: "auth/login", Name: "Login", Params: "LoginAuthRequest", Response: "LoginAuthResponse",
					CallDoc: `Login authenticates with one of the methods the agent advertised.`},
				{Wire: "auth/logout", Name: "Logout", Params: "LogoutAuthRequest", Response: "LogoutAuthResponse",
					CallDoc: `Logout clears the credentials the agent holds.`},
			},
		},
		{
			Interface: "SessionLister",
			Doc:       `SessionLister handles session/list.`,
			Methods: []Method{{
				Wire: "session/list", Name: "ListSessions", Params: "ListSessionsRequest", Response: "ListSessionsResponse",
				CallDoc: `ListSessions lists sessions, optionally filtered and paginated.`,
			}},
		},
		{
			Interface: "SessionDeleter",
			Doc: `SessionDeleter handles session/delete. Advertise it with the
` + "`capabilities.session.delete`" + ` agent capability.`,
			Methods: []Method{{
				Wire: "session/delete", Name: "DeleteSession", Params: "DeleteSessionRequest", Response: "DeleteSessionResponse",
				CallDoc: `DeleteSession deletes a session and its stored history.`,
			}},
		},
		{
			Interface:    "SessionForker",
			Experimental: true,
			Doc: `SessionForker handles session/fork. Advertise it with the
` + "`capabilities.session.fork`" + ` agent capability.`,
			Methods: []Method{{
				Wire: "session/fork", Name: "ForkSession", Params: "ForkSessionRequest", Response: "ForkSessionResponse",
				CallDoc: `ForkSession branches a session so work continues without touching the
original history.`,
			}},
		},
		{
			Interface: "SessionResumer",
			Doc: `SessionResumer handles session/resume, continuing a session without
replaying its history.`,
			Methods: []Method{{
				Wire: "session/resume", Name: "ResumeSession", Params: "ResumeSessionRequest", Response: "ResumeSessionResponse",
				CallDoc: `ResumeSession continues a session without replaying its history.`,
			}},
		},
		{
			Interface: "SessionCloser",
			Doc:       `SessionCloser handles session/close.`,
			Methods: []Method{{
				Wire: "session/close", Name: "CloseSession", Params: "CloseSessionRequest", Response: "CloseSessionResponse",
				CallDoc: `CloseSession cancels any ongoing work and frees the session's resources.`,
			}},
		},
		{
			Interface: "SessionConfigOptionSetter",
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
			Experimental: true,
			Doc: `ProviderManager handles the providers/* methods. Advertise them with the
` + "`capabilities.providers`" + ` agent capability.`,
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
			Experimental: true,
			Doc: `NesHandler handles the nes/* methods for Next Edit Suggestions. Advertise
them with the ` + "`capabilities.nes`" + ` agent capability. AcceptNes and RejectNes
are notifications.`,
			Methods: nesMethods,
		},
		{
			Interface:    "DocumentHandler",
			Experimental: true,
			Doc: `DocumentHandler receives the document/did* notifications that mirror the
client's open editors.`,
			Methods: documentMethods,
		},
		{
			Interface:    "MCPMessageHandler",
			Experimental: true,
			Doc: `MCPMessageHandler receives MCP traffic the client forwards to the agent over
mcp/message. The method carries either a request, answered with the MCP
result, or a notification, which has no response.`,
			Methods: []Method{
				{Wire: "mcp/message", Name: "MessageMCP", Params: "MessageMCPRequest", Response: "MessageMCPResponse",
					CallDoc: `MessageMCP forwards an MCP request to the agent and returns its result.`},
				{Wire: "mcp/message", Name: "NotifyMCP", Params: "MessageMCPNotification",
					CallDoc: `NotifyMCP forwards an MCP notification to the agent.`},
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
					Wire: "session/request_permission", Name: "RequestPermission", Params: "RequestPermissionRequest", Response: "RequestPermissionResponse",
					Doc: `RequestPermission asks the user to authorize a tool call. When the turn
is cancelled the client MUST answer with the cancelled outcome rather
than leaving the request pending.`,
					CallDoc: `RequestPermission asks the user to authorize a tool call.`,
				},
			},
		},
		{
			Interface:    "MCPConnector",
			Experimental: true,
			Doc: `MCPConnector lets the agent reach MCP servers through the client: mcp/connect
opens a connection, mcp/message carries requests and notifications over it,
and mcp/disconnect closes it. In v2 this replaces the v1 fs/* and terminal/*
methods.`,
			Methods: []Method{
				{Wire: "mcp/connect", Name: "ConnectMCP", Params: "ConnectMCPRequest", Response: "ConnectMCPResponse",
					CallDoc: `ConnectMCP opens an MCP connection through the client.`},
				{Wire: "mcp/message", Name: "MessageMCP", Params: "MessageMCPRequest", Response: "MessageMCPResponse",
					CallDoc: `MessageMCP sends an MCP request over a connection and returns its result.`},
				{Wire: "mcp/message", Name: "NotifyMCP", Params: "MessageMCPNotification",
					CallDoc: `NotifyMCP sends an MCP notification over a connection.`},
				{Wire: "mcp/disconnect", Name: "DisconnectMCP", Params: "DisconnectMCPRequest", Response: "DisconnectMCPResponse",
					CallDoc: `DisconnectMCP closes an MCP connection.`},
			},
		},
		{
			Interface: "ElicitationHandler",
			Doc: `ElicitationHandler handles elicitation/create and the elicitation/complete
notification. Advertise it with the ` + "`capabilities.elicitation`" + ` client
capability.`,
			Methods: elicitationMethods("capabilities.elicitation"),
		},
	},
}
