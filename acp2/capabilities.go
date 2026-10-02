package acp2

import schema "github.com/ironpark/acp-go/schema/v2"

// CapabilitiesOf derives the agent capabilities implied by the optional
// interfaces agent implements, so an Initialize response cannot advertise a
// method the connection would answer with "method not found":
//
//	caps := acp2.CapabilitiesOf(a)
//	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion, Info: info, Capabilities: caps}, nil
//
// The session object is always set: every [Agent] handles the baseline
// session methods it advertises, session/new, session/list, session/resume,
// session/close, session/prompt and session/cancel. Group capabilities with
// their own sub-flags — auth, providers and nes — are set to empty objects,
// which advertises the group; fill in the sub-flags the agent supports. [MCPMessageHandler] sets session.mcp.acp. Prompt
// capabilities, the other MCP transports and position encoding describe
// content rather than methods and are left for the agent to set.
func CapabilitiesOf(agent Agent) *AgentCapabilities {
	// Every Agent implements the baseline session methods, which an empty
	// session object advertises; the optional ones add their markers.
	session := &schema.SessionCapabilities{}
	caps := &schema.AgentCapabilities{Session: session}
	if _, ok := agent.(SessionDeleter); ok {
		session.Delete = &schema.SessionDeleteCapabilities{}
	}
	if _, ok := agent.(SessionForker); ok {
		session.Fork = &schema.SessionForkCapabilities{}
	}
	if _, ok := agent.(MCPMessageHandler); ok {
		session.MCP = &schema.MCPCapabilities{ACP: &schema.MCPACPCapabilities{}}
	}
	if _, ok := agent.(AuthHandler); ok {
		caps.Auth = &schema.AgentAuthCapabilities{}
	}
	if _, ok := agent.(ProviderManager); ok {
		caps.Providers = &schema.ProvidersCapabilities{}
	}
	if _, ok := agent.(NesHandler); ok {
		caps.Nes = &schema.NesCapabilities{}
	}
	return caps
}

// ClientCapabilitiesOf derives the client capabilities implied by the optional
// interfaces client implements: an empty elicitation object from
// [ElicitationHandler], whose form/url sub-flags the client fills in. MCP
// proxying has no client capability flag in v2. Auth, nes and position
// encoding capabilities describe what the client can display rather than which
// methods it serves and are left for the client to set.
func ClientCapabilitiesOf(client Client) *schema.ClientCapabilities {
	caps := &schema.ClientCapabilities{}
	if _, ok := client.(ElicitationHandler); ok {
		caps.Elicitation = &schema.ElicitationCapabilities{}
	}
	return caps
}
