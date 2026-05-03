// Package license provides online-only license management for elchi-backend.
// Plans cap the number of clients that may concurrently connect to the controller.
package license

import "strings"

const (
	PlanFree       = "free"
	PlanAdvance    = "advance"
	PlanEnterprise = "enterprise"
)

type PlanConfig struct {
	DisplayName string
	ClientLimit int // 0 = unlimited
}

var planConfigs = map[string]PlanConfig{
	PlanFree:       {DisplayName: "Free", ClientLimit: 1},
	PlanAdvance:    {DisplayName: "Advance", ClientLimit: 5},
	PlanEnterprise: {DisplayName: "Enterprise", ClientLimit: 0},
}

// ResolvePlan normalizes a plan string. Unknown or empty plans default to "free".
func ResolvePlan(raw string) string {
	key := strings.TrimSpace(strings.ToLower(raw))
	if key == "" {
		return PlanFree
	}
	if _, ok := planConfigs[key]; ok {
		return key
	}
	return PlanFree
}

func GetPlanConfig(plan string) PlanConfig {
	return planConfigs[ResolvePlan(plan)]
}

func GetClientLimit(plan string) int {
	return GetPlanConfig(plan).ClientLimit
}

func PlanDisplayName(plan string) string {
	return GetPlanConfig(plan).DisplayName
}
