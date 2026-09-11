package usage

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Tier is a request tier. The four values are ordered cheap < mid < strong <
// frontier; frontier is never a baseline, it is asked for explicitly.
type Tier string

const (
	TierCheap    Tier = "cheap"
	TierMid      Tier = "mid"
	TierStrong   Tier = "strong"
	TierFrontier Tier = "frontier"
)

// Provider states, as they appear in Decision.State and Alternative.State.
const (
	StateHealthy     = "healthy"
	StateConstrained = "constrained"
	StateExhausted   = "exhausted"
	StateUnknown     = "unknown"
)

// Request is one "which tool and model should this role use" question.
type Request struct {
	// Role is free text. "implementer" and "reviewer" trigger the mid floor.
	Role string
	Tier Tier
	// Prefer is a tool name. Empty means Policy.Failover[0] under the "auto"
	// tool strategy; under "default" or an empty strategy it means
	// OrchestrateToolPolicy.FallbackTool, because that strategy drops the
	// failover list entirely. See candidateTools.
	Prefer string
	// Profile is an account label; empty means the first snapshot for the
	// provider in sorted order.
	Profile string
}

// Alternative is a candidate tool that was considered and not selected.
type Alternative struct {
	Tool  string `json:"tool"`
	State string `json:"state"`
	// RemainingPercent is -1 when the state is unknown, and -1 is also a
	// reachable real reading: a negative remaining percentage is a value, not a
	// sentinel, so an exhausted candidate can report -1 too. State, not this
	// field, tells the two apart.
	RemainingPercent int `json:"remaining_percent"`
}

// Decision is the recommender's answer. Every decision is advisory: Recommend
// never returns an error and never panics on missing data.
type Decision struct {
	Tool          string        `json:"tool"`
	Provider      string        `json:"provider"`
	Model         string        `json:"model"`
	TierRequested Tier          `json:"tier_requested"`
	TierApplied   Tier          `json:"tier_applied"`
	Account       string        `json:"account"`
	State         string        `json:"state"`
	Reason        string        `json:"reason"`
	Alternatives  []Alternative `json:"alternatives"`
	FetchedAt     time.Time     `json:"fetched_at"`
}

// Reason fragments. These are literal prefixes, never format strings, so the
// tests can pin the deciding rule with a substring match and the detail can be
// appended per decision.
const (
	reasonFirstHealthy         = "selected the first healthy candidate"
	reasonPreferredConstrained = "kept the constrained preferred tool"
	reasonFirstConstrained     = "selected the first constrained candidate"
	reasonUnknownFailover      = "selected the first unknown-state failover candidate"
	reasonExhaustedCandidate   = "no eligible candidate; kept the first candidate that is not an ineligible unknown"
	reasonExhaustedFallback    = "no eligible candidate; kept the preferred tool"
	reasonNoCandidates         = "no candidate tools"
	reasonPolicyLimited        = "policy limited failover"
	reasonTierFloor            = "tier floor: role"
	reasonFrontierNoModel      = "frontier downgraded to strong: no frontier model configured"
	reasonFrontierGated        = "frontier downgraded to strong: gate window"
	reasonFrontierGateAbsent   = "frontier gate window not present in the snapshot"
	reasonProfileMiss          = "no snapshot for profile"
)

// toolProvider maps a tool name to its usage provider. Anything other than the
// two usage providers maps to none, which puts the tool in the unknown state.
// The match is exact: a miscased "Codex" has no provider, matching the config
// loader's treatment of miscased names as inert.
func toolProvider(tool string) (Provider, bool) {
	switch provider := Provider(tool); provider {
	case Claude, Codex:
		return provider, true
	default:
		return "", false
	}
}

// preferredTool resolves Request.Prefer for the "auto" strategy, defaulting to
// Policy.Failover[0]. A zero-value Policy has no Failover, so the empty result
// is possible and every caller must tolerate it. The collapsed strategies
// resolve their own preferred tool inside candidateTools; nothing outside that
// function may assume this is the prefer a decision was made against.
func preferredTool(req Request, policy Policy) string {
	if prefer := strings.TrimSpace(req.Prefer); prefer != "" {
		return prefer
	}
	if len(policy.Failover) > 0 {
		return strings.TrimSpace(policy.Failover[0])
	}
	return ""
}

// candidateTools is the design's rule 2, and the single derivation shared by
// Recommend and ProvidersToQuery so the CLI cannot drift from the recommender.
//
// It is deliberately snapshot-free: ProvidersToQuery runs BEFORE any snapshot
// exists, so nothing state-dependent (the unknown-state eligibility filter, the
// healthy-first ordering) may live here — that work happens in Recommend.
//
// The second result is the preferred tool AS THIS FUNCTION RESOLVED IT, which
// is not always preferredTool's answer: with an EMPTY Request.Prefer the
// collapsed strategies resolve to OrchestrateToolPolicy.FallbackTool, where
// preferredTool answers Policy.Failover[0]. (With Request.Prefer set the two
// agree.) Recommend must use this value and not re-derive one, or they
// disagree and the rule 3 carve-out compares against a non-candidate.
//
// The third result reports that the tool strategy limited failover to a single
// tool, which the decision's reason records. It is false when the strategy
// produced no candidate at all: "no candidate tools" and "policy limited
// failover" are contradictory as a pair, and the first is the whole story.
func candidateTools(req Request, tools session.OrchestrateToolPolicy, policy Policy) (candidates []string, prefer string, limited bool) {
	strategy := strings.TrimSpace(tools.Strategy)

	if strategy != "auto" {
		// "default" or empty: no cross-tool failover at all. The design names
		// the collapsed candidate as "[Prefer] if set, else FallbackTool", so
		// an unset Prefer yields the orchestrate fallback tool here rather than
		// Failover[0] — the failover list is exactly what this strategy drops.
		// There is deliberately no third fallback: with neither Prefer nor
		// FallbackTool set this strategy has nothing to name, and reaching for
		// Failover[0] would smuggle back the list it just dropped.
		single := strings.TrimSpace(req.Prefer)
		if single == "" {
			single = strings.TrimSpace(tools.FallbackTool)
		}
		if single == "" {
			return nil, "", false
		}
		return []string{single}, single, true
	}

	prefer = preferredTool(req, policy)

	available := make(map[string]struct{}, len(tools.AvailableTools))
	for _, tool := range tools.AvailableTools {
		available[strings.TrimSpace(tool)] = struct{}{}
	}

	seen := make(map[string]struct{}, len(policy.Failover)+1)
	add := func(tool string) {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			return
		}
		if _, duplicate := seen[tool]; duplicate {
			return
		}
		if _, ok := available[tool]; !ok {
			return
		}
		seen[tool] = struct{}{}
		candidates = append(candidates, tool)
	}
	add(prefer)
	for _, tool := range policy.Failover {
		add(tool)
	}
	return candidates, prefer, false
}

// ProvidersToQuery returns the usage providers of the candidate tools, in
// candidate order, de-duplicated, omitting tools that map to no usage provider.
// The CLI uses it to decide what to fetch before it has any snapshot.
func ProvidersToQuery(req Request, tools session.OrchestrateToolPolicy, policy Policy) []Provider {
	candidates, _, _ := candidateTools(req, tools, policy)
	var providers []Provider
	seen := make(map[Provider]struct{}, len(candidates))
	for _, tool := range candidates {
		provider, ok := toolProvider(tool)
		if !ok {
			continue
		}
		if _, duplicate := seen[provider]; duplicate {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	return providers
}

// evaluation is one candidate tool scored against the snapshots.
type evaluation struct {
	tool        string
	provider    Provider
	snapshot    *Snapshot
	state       string
	remaining   int // -1 when unknown, and also a reachable real reading; state disambiguates
	profileMiss bool
}

// selectSnapshot picks the snapshot a tool's state is read from. A named
// profile selects by account label; an empty profile takes the first account in
// sorted order (case-insensitive, matching DedupeAndSortAccounts). The snapshot
// is chosen BEFORE its state is scored, so Decision.Account always names the
// snapshot that was actually scored — including an unavailable one.
func selectSnapshot(snapshots []Snapshot, provider Provider, profile string) (*Snapshot, bool) {
	matches := make([]*Snapshot, 0, len(snapshots))
	for i := range snapshots {
		if snapshots[i].Provider != provider {
			continue
		}
		if profile != "" && snapshots[i].Account != profile {
			continue
		}
		matches = append(matches, &snapshots[i])
	}
	if len(matches) == 0 {
		return nil, false
	}
	sort.SliceStable(matches, func(i, j int) bool {
		left, right := strings.ToLower(matches[i].Account), strings.ToLower(matches[j].Account)
		if left != right {
			return left < right
		}
		return matches[i].Account < matches[j].Account
	})
	return matches[0], true
}

// snapshotState is the design's rule 1: remaining is the minimum over the
// Session5H and Weekly windows PRESENT on the snapshot.
//
// Both comparisons are STRICT. remaining == ExhaustedBelow is constrained, not
// exhausted; remaining == ConstrainedBelow is healthy, not constrained. Equal
// thresholds are legal and make "constrained" unreachable: everything below the
// shared value is exhausted and everything at or above it is healthy.
//
// "Present" is tracked with a separate flag rather than a negative sentinel:
// parseWindow never clamps, so a negative RemainingPercent is a value a
// snapshot may legitimately carry, and folding the two meanings together would
// silently drop it from the minimum.
func snapshotState(snapshot *Snapshot, policy Policy) (state string, remaining int) {
	if snapshot == nil || !snapshot.Available {
		return StateUnknown, -1
	}
	found := false
	for _, window := range []*Window{snapshot.Windows.Session5H, snapshot.Windows.Weekly} {
		if window == nil {
			continue
		}
		if !found || window.RemainingPercent < remaining {
			remaining, found = window.RemainingPercent, true
		}
	}
	if !found {
		return StateUnknown, -1
	}
	switch {
	case remaining < policy.ExhaustedBelow:
		return StateExhausted, remaining
	case remaining < policy.ConstrainedBelow:
		return StateConstrained, remaining
	default:
		return StateHealthy, remaining
	}
}

func evaluateTool(tool string, snapshots []Snapshot, profile string, policy Policy) evaluation {
	eval := evaluation{tool: tool, state: StateUnknown, remaining: -1}
	provider, ok := toolProvider(tool)
	if !ok {
		// A named profile misses here too: a tool with no usage provider has
		// no snapshot for any profile. Recording it keeps the clause from
		// being silently provider-scoped, which would let a request name a
		// profile that matched nothing and say nothing about it.
		eval.profileMiss = profile != ""
		return eval
	}
	eval.provider = provider
	snapshot, found := selectSnapshot(snapshots, provider, profile)
	if !found {
		eval.profileMiss = profile != ""
		return eval
	}
	eval.snapshot = snapshot
	eval.state, eval.remaining = snapshotState(snapshot, policy)
	return eval
}

// eligibleUnknown is the design's rule 2 tail: a tool in the unknown state is
// eligible only when it appears in Policy.Failover explicitly. Failover holds
// TOOL names validated by shape only, so an entry naming no usage provider is
// exactly what makes this rule reachable.
func eligibleUnknown(tool string, policy Policy) bool {
	for _, entry := range policy.Failover {
		if strings.TrimSpace(entry) == tool {
			return true
		}
	}
	return false
}

// selectCandidate is the design's rule 3.
//
// The carve-out is evaluated BEFORE the healthy step, which is a deliberate
// reading of the design: rule 3 lists "first healthy candidate" first, but
// `## Decisions` scopes the carve-out as "avoided when a healthy candidate
// exists, BUT a constrained preferred tool still wins for strong/frontier
// tiers". The invariant that makes the literal ordering dead code is that the
// preferred tool is never anywhere but the FIRST position: candidateTools
// either drops it (an "auto" strategy filtering it as unavailable) or puts it
// at index 0. So under the literal ordering the carve-out could only ever
// match candidates[0], which the "first constrained candidate" step already
// returns — it would be unreachable. This order is the only one in which it
// does work.
//
// prefer must be the value candidateTools RESOLVED, not preferredTool's, or
// the invariant above does not hold: on the collapsed path with an empty
// Request.Prefer the resolved tool is OrchestrateToolPolicy.FallbackTool while
// preferredTool answers Policy.Failover[0], and the carve-out would then
// compare against a tool that is not a candidate at all and silently fall
// through to the weaker "first constrained candidate" rule.
//
// tier is the APPLIED tier: the implementer/reviewer floor is applied first, so
// the carve-out sees the tier the launch will actually use. (The floor only
// ever raises cheap to mid, so it can neither create nor destroy a carve-out.)
//
// It returns -1 when no candidate is eligible.
func selectCandidate(evals []evaluation, prefer string, tier Tier, policy Policy) (int, string) {
	if tier == TierStrong || tier == TierFrontier {
		for i, eval := range evals {
			if eval.tool == prefer && eval.state == StateConstrained {
				return i, reasonPreferredConstrained + fmt.Sprintf(" %q for the %s tier", eval.tool, tier)
			}
		}
	}
	for i, eval := range evals {
		if eval.state == StateHealthy {
			return i, reasonFirstHealthy
		}
	}
	for i, eval := range evals {
		if eval.state == StateConstrained {
			return i, reasonFirstConstrained
		}
	}
	for i, eval := range evals {
		if eval.state == StateUnknown && eligibleUnknown(eval.tool, policy) {
			return i, reasonUnknownFailover
		}
	}
	return -1, ""
}

func ladderRung(ladder Ladder, tier Tier) string {
	switch tier {
	case TierCheap:
		return ladder.Cheap
	case TierMid:
		return ladder.Mid
	case TierStrong:
		return ladder.Strong
	case TierFrontier:
		return ladder.Frontier
	default:
		return ""
	}
}

// resolveModel is the design's rule 5. An empty ladder entry for the applied
// tier yields an empty model — the CLI omits the flag — rather than a guess.
//
// The frontier gate downgrades to strong when the ladder has no frontier entry,
// or when the provider's FrontierWindow names a window PRESENT in the snapshot
// whose remaining is below ConstrainedBelow (strictly: a window sitting exactly
// at ConstrainedBelow does not gate, matching the state comparison). A gate
// window that is absent from Snapshot.Windows.Models does NOT gate, but the
// reason records that so the user can see why the gate did not apply. The two
// downgrade causes are independent and both are reported when both hold.
//
// Two carve-outs out of rule 5, both silent — they add no reason clause:
//
//   - A tool with NO usage provider (provider == "") has no ladder to read, so
//     the model is empty and the requested tier echoes into TierApplied
//     unchanged. A frontier request for such a tool therefore reports
//     TierApplied == frontier with Model == "", where a real provider with an
//     empty frontier rung would have reported strong. There is nothing to
//     downgrade to and no evidence to downgrade on.
//   - A Tier outside the four constants has no rung, so ladderRung returns ""
//     and that tier echoes into TierApplied as well. The CLI rejects an
//     unknown tier before it reaches here (exit 2), so this is reachable only
//     through the exported Go API.
func resolveModel(provider Provider, tier Tier, snapshot *Snapshot, policy Policy) (string, Tier, []string) {
	if provider == "" {
		return "", tier, nil
	}
	ladder := policy.Ladder[provider]
	if tier != TierFrontier {
		return ladderRung(ladder, tier), tier, nil
	}

	// Both downgrade causes are evaluated, not just the first: an empty
	// frontier rung does not short-circuit the gate, so when both hold the
	// reason names both rather than hiding one behind the other.
	var clauses []string
	frontier := ladderRung(ladder, TierFrontier)
	if frontier == "" {
		clauses = append(clauses, reasonFrontierNoModel+fmt.Sprintf(" for %s", provider))
	}

	gated := false
	if window := strings.TrimSpace(policy.FrontierWindow[provider]); window != "" {
		var gate *Window
		if snapshot != nil {
			gate = snapshot.Windows.Models[window]
		}
		switch {
		case gate == nil:
			clauses = append(clauses, reasonFrontierGateAbsent+
				fmt.Sprintf(" %q; the gate did not apply", window))
		case gate.RemainingPercent < policy.ConstrainedBelow:
			gated = true
			clauses = append(clauses, reasonFrontierGated+
				fmt.Sprintf(" %q at %d%% is below constrained_below %d",
					window, gate.RemainingPercent, policy.ConstrainedBelow))
		}
	}

	if frontier == "" || gated {
		return ladderRung(ladder, TierStrong), TierStrong, clauses
	}
	return frontier, TierFrontier, clauses
}

// applyTierFloor is the design's rule 4: the implementer and reviewer roles
// asking for cheap are raised to mid. Other roles asking for cheap are not.
func applyTierFloor(role string, tier Tier) (Tier, string) {
	if tier != TierCheap {
		return tier, ""
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "implementer", "reviewer":
		return TierMid, reasonTierFloor + fmt.Sprintf(" %q raised the cheap tier to mid", role)
	default:
		return tier, ""
	}
}

// Recommend answers one request. It is pure: no I/O, no clock read beyond
// copying a snapshot's FetchedAt, no config loading. It never returns an error
// and never panics on missing data — an absent snapshot, Available == false and
// a tool with no usage provider all resolve to the unknown state.
func Recommend(req Request, snapshots []Snapshot, tools session.OrchestrateToolPolicy, policy Policy) Decision {
	candidates, prefer, limited := candidateTools(req, tools, policy)
	profile := strings.TrimSpace(req.Profile)

	// Rule 4 runs before rule 3 because the carve-out reads the applied tier.
	applied, floorClause := applyTierFloor(req.Role, req.Tier)

	evals := make([]evaluation, 0, len(candidates))
	for _, tool := range candidates {
		evals = append(evals, evaluateTool(tool, snapshots, profile, policy))
	}

	// The tool rule 3's tail falls back to, in strict precedence:
	//
	//  1. the first candidate that is not an INELIGIBLE unknown — an exhausted
	//     candidate outranks an unknown one that Policy.Failover does not list,
	//     because selecting the latter would contradict rule 2's "not listed ->
	//     never selected";
	//  2. otherwise the resolved preferred tool.
	//
	// The `default:` arm below is that tail: it is the arm reached when no
	// candidate is eligible — every candidate exhausted, an ineligible unknown,
	// or a mix. Step 1 answers all of those but one: the loop's break takes any
	// eval that is not an ineligible unknown, so step 2 runs only when every
	// candidate is an ineligible unknown, or there are none at all. The two
	// steps therefore record DIFFERENT reasons —
	// reasonExhaustedCandidate for step 1, reasonExhaustedFallback for step 2 —
	// and the empty candidate list, though its tool also comes from step 2, has
	// an arm and a reason of its own, reasonNoCandidates.
	//
	// Step 2 is the design's "the preferred tool with State = exhausted"
	// fallback. The tool it names is NOT filtered against the orchestrate
	// policy: it may be absent from AvailableTools, may itself be an ineligible
	// unknown, may have no snapshot, and may be "" — a collapsed strategy that
	// resolves neither Request.Prefer nor FallbackTool names no tool at all,
	// and that is reported as-is rather than guarded here.
	//
	// The consequence a caller must handle is that ProvidersToQuery can return
	// no providers for the very input whose decision names this tool.
	// ProvidersToQuery is empty exactly when no candidate maps to a usage
	// provider; for instance AvailableTools can filter the named tool out of
	// the candidate list (reachable under "auto"), and on a collapsed strategy
	// whose single rung did name a tool, that sole candidate mapping to no
	// usage provider is the only mechanism left. Either way a caller that
	// fetches first will hold no snapshot for it.
	lastResort := prefer
	fromCandidate := false
	for _, eval := range evals {
		if eval.state == StateUnknown && !eligibleUnknown(eval.tool, policy) {
			continue
		}
		lastResort = eval.tool
		fromCandidate = true
		break
	}

	index, rule := selectCandidate(evals, prefer, applied, policy)
	var selected evaluation
	switch {
	case index >= 0:
		selected = evals[index]
	case len(candidates) == 0:
		// Nothing to choose from: report the resolved preferred tool, which is
		// empty when the strategy resolved no tool at all.
		selected = evaluateTool(lastResort, snapshots, profile, policy)
		rule = reasonNoCandidates + fmt.Sprintf(" for tool strategy %q", strings.TrimSpace(tools.Strategy))
	default:
		// Rule 3's tail. The fallback tool is reported with the state it
		// actually has: exhausted when a snapshot says so, unknown when there
		// is no snapshot at all (a missing openusage never becomes exhausted).
		// fromCandidate, not lastResort == prefer, is what says which step
		// assigned: the first surviving candidate may itself BE the preferred
		// tool, and the two steps are different rules with different reasons.
		selected = evaluateTool(lastResort, snapshots, profile, policy)
		rule = reasonExhaustedFallback
		if fromCandidate {
			rule = reasonExhaustedCandidate
		}
	}

	model, applied, modelClauses := resolveModel(selected.provider, applied, selected.snapshot, policy)

	clauses := []string{rule}
	if limited {
		clauses = append(clauses, reasonPolicyLimited+fmt.Sprintf(" (tool strategy %q)", strings.TrimSpace(tools.Strategy)))
	}
	if selected.profileMiss {
		clauses = append(clauses, reasonProfileMiss+fmt.Sprintf(" %q", profile))
	}
	if floorClause != "" {
		clauses = append(clauses, floorClause)
	}
	clauses = append(clauses, modelClauses...)

	alternatives := make([]Alternative, 0, len(evals))
	for _, eval := range evals {
		if eval.tool == selected.tool {
			continue
		}
		alternatives = append(alternatives, Alternative{
			Tool:             eval.tool,
			State:            eval.state,
			RemainingPercent: eval.remaining,
		})
	}

	decision := Decision{
		Tool:          selected.tool,
		Provider:      string(selected.provider),
		Model:         model,
		TierRequested: req.Tier,
		TierApplied:   applied,
		State:         selected.state,
		Reason:        strings.Join(clauses, "; "),
		Alternatives:  alternatives,
	}
	if selected.snapshot != nil {
		decision.Account = selected.snapshot.Account
		decision.FetchedAt = selected.snapshot.FetchedAt
	}
	return decision
}
