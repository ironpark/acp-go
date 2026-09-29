package acp1

import "fmt"

// SelectOptions lists the choices of a select config option. Unlike
// [NewSessionConfigSelectOptions], it takes the flat list directly and cannot
// fail:
//
//	acp1.NewSessionConfigOption(acp1.SessionConfigOptionSelect{
//		ID: "model", Name: "Model", CurrentValue: "fast",
//		Options: acp1.SelectOptions(
//			acp1.SessionConfigSelectOption{Value: "fast", Name: "Fast"},
//			acp1.SessionConfigSelectOption{Value: "smart", Name: "Smart"},
//		),
//	})
func SelectOptions(choices ...SessionConfigSelectOption) SessionConfigSelectOptions {
	return mustSelectOptions(NewSessionConfigSelectOptions(append([]SessionConfigSelectOption{}, choices...)))
}

// SelectOptionGroups lists the choices of a select config option in groups.
func SelectOptionGroups(groups ...SessionConfigSelectGroup) SessionConfigSelectOptions {
	return mustSelectOptions(NewSessionConfigSelectOptions(append([]SessionConfigSelectGroup{}, groups...)))
}

// mustSelectOptions unwraps the choices of a select option, which encode
// whenever the list is not nil.
func mustSelectOptions(options SessionConfigSelectOptions, err error) SessionConfigSelectOptions {
	if err != nil {
		panic(fmt.Sprintf("acp1: encode select options: %v", err))
	}
	return options
}

// ConfigChange is the change a session/set_config_option request asks for,
// whichever kind of option it sets.
type ConfigChange struct {
	SessionID SessionID
	ConfigID  SessionConfigID
	// Value is the value chosen for a select option.
	Value SessionConfigValueID
	// Boolean is the value set on a boolean option, or nil for a select one.
	Boolean *bool
}

// ConfigChangeOf reads a session/set_config_option request without a type
// switch over its variants. It reports false for a request that holds no
// variant, which a handler rejects as invalid:
//
//	func (a *myAgent) SetSessionConfigOption(ctx context.Context, params *acp1.SetSessionConfigOptionRequest) (*acp1.SetSessionConfigOptionResponse, error) {
//		change, ok := acp1.ConfigChangeOf(params)
//		if !ok || change.Boolean != nil {
//			return nil, acp.InvalidParams("expected a select value")
//		}
//		...
//	}
func ConfigChangeOf(params *SetSessionConfigOptionRequest) (ConfigChange, bool) {
	switch v := params.Variant().(type) {
	case SetSessionConfigOptionRequestUntagged:
		return ConfigChange{SessionID: v.SessionID, ConfigID: v.ConfigID, Value: v.Value}, true
	case SetSessionConfigOptionRequestBoolean:
		return ConfigChange{SessionID: v.SessionID, ConfigID: v.ConfigID, Boolean: &v.Value}, true
	}
	return ConfigChange{}, false
}
