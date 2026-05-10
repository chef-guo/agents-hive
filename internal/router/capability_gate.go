package router

import "slices"

type CapabilityGateInput struct {
	IntentRequired []Capability
	ToolGranted    []Capability
	SessionGranted []Capability
	PlanAllowed    []Capability
	Deny           []Capability
}

type CapabilityGateResult struct {
	Allowed bool
	Reason  string
	Missing []Capability
	Denied  []Capability
}

func RequiredCapabilitiesForIntent(intent IntentFrame) []Capability {
	rule, ok := intentCapabilityRule(intent.Kind)
	if !ok {
		return nil
	}
	return rule.Required
}

func ProfileHasSideEffect(profile ToolProfile) bool {
	if profile.SideEffect || profile.Destructive || profile.OpenWorld {
		return true
	}
	switch profile.Risk {
	case RiskLocalWrite, RiskExternalWrite, RiskRuntimeExec, RiskDestructive, RiskUnknown:
		return true
	default:
		return len(profile.Capabilities) > 0 && !profile.ReadOnly
	}
}

func CheckCapabilityGate(input CapabilityGateInput) CapabilityGateResult {
	required := uniqueCapabilities(input.IntentRequired)
	if len(required) == 0 {
		return CapabilityGateResult{Allowed: true}
	}
	denied := intersectCapabilities(required, input.Deny)
	if len(denied) > 0 {
		return CapabilityGateResult{Allowed: false, Reason: "capability denied", Denied: denied}
	}
	available := intersectionOrAll(input.ToolGranted, input.SessionGranted)
	available = intersectionOrAll(available, input.PlanAllowed)
	missing := missingCapabilities(required, available)
	if len(missing) > 0 {
		return CapabilityGateResult{Allowed: false, Reason: "capability missing", Missing: missing}
	}
	return CapabilityGateResult{Allowed: true}
}

func ToolCapabilitiesFromProfile(profile ToolProfile) []Capability {
	return uniqueCapabilities(profile.Capabilities)
}

func uniqueCapabilities(in []Capability) []Capability {
	if len(in) == 0 {
		return nil
	}
	out := make([]Capability, 0, len(in))
	for _, item := range in {
		if item == "" || slices.Contains(out, item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func intersectionOrAll(left, right []Capability) []Capability {
	left = uniqueCapabilities(left)
	right = uniqueCapabilities(right)
	if len(left) == 0 {
		return right
	}
	if len(right) == 0 {
		return left
	}
	return intersectCapabilities(left, right)
}

func intersectCapabilities(left, right []Capability) []Capability {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	out := make([]Capability, 0, len(left))
	for _, item := range left {
		if slices.Contains(right, item) && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func missingCapabilities(required, available []Capability) []Capability {
	var missing []Capability
	for _, item := range uniqueCapabilities(required) {
		if !slices.Contains(available, item) {
			missing = append(missing, item)
		}
	}
	return missing
}
