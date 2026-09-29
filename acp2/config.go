package acp2

import "fmt"

// SelectOptions lists the choices of a select config option. Unlike
// [NewSessionConfigSelectOptions], it takes the flat list directly and cannot
// fail:
//
//	acp2.NewSessionConfigOption(acp2.SessionConfigOptionSelect{
//		ID: "model", Name: "Model", CurrentValue: "fast",
//		Options: acp2.SelectOptions(
//			acp2.SessionConfigSelectOption{Value: "fast", Name: "Fast"},
//			acp2.SessionConfigSelectOption{Value: "smart", Name: "Smart"},
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
		panic(fmt.Sprintf("acp2: encode select options: %v", err))
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
	// Boolean is the value set on a boolean option, or nil for another kind.
	Boolean *bool
	// Custom is the request for an option of a custom type, or nil for a
	// select or boolean one.
	Custom *SetSessionConfigOptionRequestCustom
}

// ConfigChangeOf reads a session/set_config_option request without a type
// switch over its variants. It reports false for a request that holds no
// variant, which a handler rejects as invalid.
func ConfigChangeOf(params *SetSessionConfigOptionRequest) (ConfigChange, bool) {
	switch v := params.Variant().(type) {
	case SetSessionConfigOptionRequestID:
		return ConfigChange{SessionID: v.SessionID, ConfigID: v.ConfigID, Value: v.Value}, true
	case SetSessionConfigOptionRequestBoolean:
		return ConfigChange{SessionID: v.SessionID, ConfigID: v.ConfigID, Boolean: &v.Value}, true
	case SetSessionConfigOptionRequestCustom:
		return ConfigChange{SessionID: v.SessionID, ConfigID: v.ConfigID, Custom: &v}, true
	}
	return ConfigChange{}, false
}
