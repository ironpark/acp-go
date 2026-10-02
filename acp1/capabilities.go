package acp1

// CapabilitiesOf derives the agent capabilities implied by the optional
// interfaces agent implements, so an Initialize response cannot advertise a
// method the connection would answer with "method not found":
//
//	caps := acp1.CapabilitiesOf(a)
//	caps.PromptCapabilities = &acp1.PromptCapabilities{Image: new(true)}
//	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion, AgentCapabilities: caps}, nil
//
// Group capabilities with their own sub-flags — providers and nes — are set to
// empty objects, which advertises the group; fill in the sub-flags the agent
// supports. [LogoutHandler] sets auth.logout, and [MCPMessageHandler]
// mcpCapabilities.acp. Prompt capabilities, the other MCP transports and
// position encoding describe content rather than methods and are left for the
// agent to set.
func CapabilitiesOf(agent Agent) *AgentCapabilities {
	caps := &AgentCapabilities{}
	capabilitiesOf(agent, caps)
	return caps
}

// ClientCapabilitiesOf derives the client capabilities implied by the optional
// interfaces client implements: fs.readTextFile from [FileReader],
// fs.writeTextFile from [FileWriter], terminal from [TerminalHandler], and an empty elicitation
// object from [ElicitationHandler], whose form/url sub-flags the client fills
// in. Session, plan, auth, nes and position encoding capabilities describe
// what the client can display rather than which methods it serves and are
// left for the client to set.
func ClientCapabilitiesOf(client Client) *ClientCapabilities {
	caps := &ClientCapabilities{}
	clientCapabilitiesOf(client, caps)
	return caps
}
