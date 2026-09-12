package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/usage"
)

// usageRecommendTiers is the closed set of --tier values. A tier the map does
// not hold is a flag error; the design gives no default tier.
var usageRecommendTiers = map[string]usage.Tier{
	string(usage.TierCheap):    usage.TierCheap,
	string(usage.TierMid):      usage.TierMid,
	string(usage.TierStrong):   usage.TierStrong,
	string(usage.TierFrontier): usage.TierFrontier,
}

const usageRecommendUsageLine = "Usage: agent-deck usage recommend --role <role> --tier <cheap|mid|strong|frontier> [--prefer <tool>] [--profile <name>] [--json]"

// usageRecommendJSON mirrors usage.Decision's JSON shape with one deliberate
// change: FetchedAt is a pointer, so a decision made with no snapshot at all
// renders `"fetched_at": null` instead of the zero time's
// "0001-01-01T00:00:00Z", which reads as a real timestamp. The key is still
// always present.
//
// TestUsageRecommendJSONMirrorsDecisionKeys pins this struct's JSON key set
// against usage.Decision's, so a field added there cannot be silently dropped
// here.
type usageRecommendJSON struct {
	Tool          string              `json:"tool"`
	Provider      string              `json:"provider"`
	Model         string              `json:"model"`
	TierRequested usage.Tier          `json:"tier_requested"`
	TierApplied   usage.Tier          `json:"tier_applied"`
	Account       string              `json:"account"`
	State         string              `json:"state"`
	Reason        string              `json:"reason"`
	Alternatives  []usage.Alternative `json:"alternatives"`
	FetchedAt     *time.Time          `json:"fetched_at"`
}

func usageRecommendPayload(decision usage.Decision) usageRecommendJSON {
	payload := usageRecommendJSON{
		Tool:          decision.Tool,
		Provider:      decision.Provider,
		Model:         decision.Model,
		TierRequested: decision.TierRequested,
		TierApplied:   decision.TierApplied,
		Account:       decision.Account,
		State:         decision.State,
		Reason:        decision.Reason,
		Alternatives:  decision.Alternatives,
	}
	if !decision.FetchedAt.IsZero() {
		fetched := decision.FetchedAt
		payload.FetchedAt = &fetched
	}
	return payload
}

// usageRecommendRemaining renders an alternative's remaining percentage.
//
// usage.Alternative.RemainingPercent is -1 when the state is unknown, and -1 is
// also a reading a provider can genuinely report, so the number alone cannot
// tell the two apart — State can. The unknown state therefore renders "-" and
// every other state renders the number it carries, negative included.
func usageRecommendRemaining(alt usage.Alternative) string {
	if alt.State == usage.StateUnknown {
		return "-"
	}
	return strconv.Itoa(alt.RemainingPercent)
}

// renderUsageRecommendText writes the design's text form: the headline, the
// reason, then the alternatives table. Decision.Reason is passed through
// verbatim — rewording it here would mean matching on the recommender's reason
// strings, which is the drift the CLI is built to avoid.
func renderUsageRecommendText(out io.Writer, decision usage.Decision) {
	headline := []string{}
	// An empty tool is a real answer too ("no candidate tool for this
	// invocation"), and printing it would put a stray empty field at the head
	// of the line, so it is omitted the same way an empty model is.
	if decision.Tool != "" {
		headline = append(headline, decision.Tool)
	}
	// An empty model is a real answer ("no model configured for the applied
	// tier"), so the field is omitted rather than printed empty.
	if decision.Model != "" {
		headline = append(headline, decision.Model)
	}
	headline = append(headline,
		fmt.Sprintf("tier=%s", decision.TierApplied),
		fmt.Sprintf("state=%s", decision.State))
	fmt.Fprintln(out, strings.Join(headline, " "))
	fmt.Fprintln(out, decision.Reason)

	if len(decision.Alternatives) == 0 {
		return
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "tool\tstate\tremaining%")
	for _, alt := range decision.Alternatives {
		fmt.Fprintf(table, "%s\t%s\t%s\n", alt.Tool, alt.State, usageRecommendRemaining(alt))
	}
	_ = table.Flush()
}

// usageRecommendSnapshots queries exactly the providers usage.ProvidersToQuery
// names, in the account order configuredUsageAccounts already establishes. The
// candidate rule lives in the recommender; deriving the provider set here would
// let the two drift.
//
// A failed query becomes an unavailable snapshot rather than an error: a
// missing openusage is the case this command exists to answer, and the
// recommender scores an unavailable snapshot as the unknown state.
func usageRecommendSnapshots(req usage.Request, config *session.UserConfig, tools session.OrchestrateToolPolicy, policy usage.Policy) []usage.Snapshot {
	providers := usage.ProvidersToQuery(req, tools, policy)
	if len(providers) == 0 {
		return nil
	}
	wanted := make(map[usage.Provider]struct{}, len(providers))
	for _, provider := range providers {
		wanted[provider] = struct{}{}
	}
	runner := usage.Runner{}
	snapshots := []usage.Snapshot{}
	for _, account := range configuredUsageAccounts(config) {
		if _, ok := wanted[account.Provider]; !ok {
			continue
		}
		snapshot, err := runner.Query(context.Background(), account)
		if err != nil {
			snapshots = append(snapshots, usage.Snapshot{Provider: account.Provider, Account: account.Label, Error: err.Error()})
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

// handleUsageRecommend implements `agent-deck usage recommend`. Exit codes are
// advisory: every decision the recommender reaches exits 0, including the
// exhausted and unknown states, a completely missing openusage binary, and a
// decision that names no tool at all. Exit 2 is reserved for an invocation
// this command cannot act on, which is wider than a bad flag value: a missing
// --role, an unknown --tier, an unknown --prefer tool, flag.Parse's own
// rejections, and a stray positional argument — which is not a flag at all,
// since flag.Parse accepts it and leaves it in NArg(). All five are driven by
// TestUsageRecommendRejectsBadFlags' table. Exit 1 is a third case and not an
// invocation error at all: it reports this command's own machinery failing,
// on the three os.Exit(1) paths below (config load, policy merge, JSON
// encode).
func handleUsageRecommend(args []string) {
	fs := flag.NewFlagSet("usage recommend", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	role := fs.String("role", "", "requesting role, e.g. implementer")
	tier := fs.String("tier", "", "cheap|mid|strong|frontier")
	prefer := fs.String("prefer", "", "preferred tool name")
	profile := fs.String("profile", "", "usage account label")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageRecommendUsageLine)
	}
	if err := fs.Parse(args); err != nil {
		if err != flag.ErrHelp {
			os.Exit(2)
		}
		return
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "usage recommend takes flags only, got %q\n", fs.Arg(0))
		os.Exit(2)
	}

	roleValue := strings.TrimSpace(*role)
	if roleValue == "" {
		fmt.Fprintln(os.Stderr, "usage recommend: --role is required")
		os.Exit(2)
	}
	tierValue, ok := usageRecommendTiers[strings.TrimSpace(*tier)]
	if !ok {
		fmt.Fprintf(os.Stderr, "usage recommend: --tier %q is not one of cheap, mid, strong, frontier\n", strings.TrimSpace(*tier))
		os.Exit(2)
	}

	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage recommend: load config: %v\n", err)
		os.Exit(1)
	}
	// A PolicyFromConfig error is fatal: its zero Policy has no thresholds, no
	// ladder and no failover list, so continuing would answer from a policy the
	// user never configured.
	policy, policyErr := usage.PolicyFromConfig(config)
	if policyErr != nil {
		fmt.Fprintf(os.Stderr, "usage recommend: %v\n", policyErr)
		os.Exit(1)
	}

	// The registry check runs only for a non-empty --prefer: an omitted
	// --prefer is the normal path, where the preferred tool comes from the
	// policy instead, and Get("") is nil for a reason that is not a flag error.
	preferValue := strings.TrimSpace(*prefer)
	if preferValue != "" && session.Init(config.Tools).Get(preferValue) == nil {
		fmt.Fprintf(os.Stderr, "usage recommend: --prefer %q is not a known tool\n", preferValue)
		os.Exit(2)
	}

	tools := config.ResolveOrchestrateToolPolicy()
	req := usage.Request{
		Role:    roleValue,
		Tier:    tierValue,
		Prefer:  preferValue,
		Profile: strings.TrimSpace(*profile),
	}
	decision := usage.Recommend(req, usageRecommendSnapshots(req, config, tools, policy), tools, policy)

	// The recommender names no tool when the invocation named none either:
	// under a "default" or empty tool strategy with neither --prefer nor
	// [default_tool] there is no candidate to score. That is still a decision
	// — no flag was wrong — so it renders on stdout like any other and exits
	// 0; the remedy goes to stderr, written before stdout is touched so a
	// terminal cannot interleave it into the decision.
	if decision.Tool == "" {
		fmt.Fprintf(os.Stderr, "usage recommend: no candidate tool (%s); pass --prefer <tool> or set default_tool in config.toml\n", decision.Reason)
	}

	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(usageRecommendPayload(decision)); err != nil {
			fmt.Fprintf(os.Stderr, "usage recommend: encode json: %v\n", err)
			os.Exit(1)
		}
		return
	}
	renderUsageRecommendText(os.Stdout, decision)
}
