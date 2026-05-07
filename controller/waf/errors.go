package waf

import "fmt"

// ErrWAFNameTaken is returned when a Create or Update would collide with the
// {project, name} unique index. Handlers translate this to HTTP 409 with code
// "WAF_NAME_TAKEN".
var ErrWAFNameTaken = fmt.Errorf("waf config name already exists in this project")

// ErrWAFIdentityImmutable is returned when an Update tries to change Name or
// Project. Both are referenced by WASM extensions via {project, name}; renames
// would orphan those references and silently break the data plane. Handlers
// translate this to HTTP 400 with code "WAF_IDENTITY_IMMUTABLE".
var ErrWAFIdentityImmutable = fmt.Errorf("waf config name and project are immutable; create a new config and migrate WASM extensions instead")

// WAFUsageRef identifies a single resource that references a WAF config and
// blocks its deletion.
type WAFUsageRef struct {
	Type string `json:"type"` // currently always "wasm_extension"
	ID   string `json:"id"`
	Name string `json:"name"`
}

// WAFInUseError is returned by Delete when one or more WASM extensions still
// reference the WAF config. Handlers translate this to HTTP 409 with code
// "WAF_IN_USE" and serialize References for the FE to render deep links.
type WAFInUseError struct {
	Name       string
	References []WAFUsageRef
}

// Error implements the error interface.
func (e *WAFInUseError) Error() string {
	return fmt.Sprintf("WAF config '%s' is referenced by %d resource(s)", e.Name, len(e.References))
}
