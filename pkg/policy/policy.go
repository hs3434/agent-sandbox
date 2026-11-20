// Package policy defines the config-class resource: network rules, command
// rules, quotas, and time limits (design-spec-v3 §8). Versioned; recovery
// via version rollback. P1 scope per roadmap — skeleton keeps only the
// core decision type; stores/versioning/engines land with their consumers.
//
// Recovery-first: the command policy default is EMPTY (no commands blocked)
// — capability is maximized, safety comes from snapshot/revert.
package policy

// Kind enumerates policy types (P0 uses KindCommand only).
type Kind string

const KindCommand Kind = "command"

// Rule is one allow/deny rule.
type Rule struct {
	Action string // "allow" | "deny"
	Target string
}

// Policy is a versioned policy resource.
type Policy struct {
	ID      string
	Kind    Kind
	Version int
	Rules   []Rule
}

// Decision evaluates a target against the rules: first matching rule wins;
// no match falls through to allow (recovery-first default).
func (p *Policy) Decision(target string) bool {
	for _, r := range p.Rules {
		if r.Target == target {
			return r.Action != "deny"
		}
	}
	return true
}
