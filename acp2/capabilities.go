package acp2

// CapabilitiesOf derives the agent capabilities implied by the optional
// interfaces agent implements, so an Initialize response cannot advertise a
// method the connection would answer with "method not found":
//
//	caps := acp2.CapabilitiesOf(a)
//	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion, Info: info, Capabilities: caps}, nil
//
// The session object is always set: every [Agent] handles the baseline
// session methods it advertises, session/new, session/list, session/resume,
// session/close, session/prompt and session/cancel. Providers, a group
// capability with its own sub-flags, is set to an empty object, which
// advertises the group; fill in the sub-flags the agent supports.
// [SessionArchiver], [SessionStatusReader], [SessionTitleSetter] and
// [SessionOptionsReader] set session.archive, session.status, session.title
// and session.options, [AuthStatusReader] auth.status, and
// [MCPMessageHandler] session.mcp.acp. An agent advertising session.options
// also accepts the initial config option values of session setup requests.
// [AuthHandler] has no capability: a non-empty authMethods list advertises
// auth/login and auth/logout. [ToolCallCanceller] and [TerminalCanceller]
// have none either: each tool call and terminal advertises its own
// capabilities.cancel. Prompt capabilities and the other MCP transports
// describe content rather than methods and are left for the agent to set.
func CapabilitiesOf(agent Agent) *AgentCapabilities {
	caps := &AgentCapabilities{}
	capabilitiesOf(agent, caps)
	return caps
}

// ClientCapabilitiesOf derives the client capabilities implied by the optional
// interfaces client implements: an empty elicitation object from
// [ElicitationHandler], whose form/url sub-flags the client fills in. MCP
// proxying has no client capability flag in v2. Auth capabilities describe
// what the client can display rather than which methods it serves and are left
// for the client to set.
func ClientCapabilitiesOf(client Client) *ClientCapabilities {
	caps := &ClientCapabilities{}
	clientCapabilitiesOf(client, caps)
	return caps
}
