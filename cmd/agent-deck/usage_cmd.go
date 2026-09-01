package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/usage"
)

func configuredUsageAccounts(config *session.UserConfig) []usage.Account {
	if config == nil {
		return nil
	}
	accounts := []usage.Account{}
	add := func(provider usage.Provider, home, label string) {
		home = usage.CanonicalHome(session.ExpandPath(home))
		if home != "" {
			accounts = append(accounts, usage.Account{Provider: provider, Home: home, Label: label})
		}
	}
	add(usage.Claude, config.Claude.ConfigDir, "Claude")
	add(usage.Codex, config.Codex.ConfigDir, "Codex")
	add(usage.Claude, usage.DefaultHome(usage.Claude), "Claude")
	add(usage.Codex, usage.DefaultHome(usage.Codex), "Codex")
	for name := range config.Profiles {
		add(usage.Claude, config.GetProfileClaudeConfigDir(name), name)
		add(usage.Codex, config.GetProfileCodexConfigDir(name), name)
	}
	if home := os.Getenv("CLAUDE_CONFIG_DIR"); home != "" {
		add(usage.Claude, home, "Claude")
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		add(usage.Codex, home, "Codex")
	}
	seen := map[string]bool{}
	out := make([]usage.Account, 0, len(accounts))
	for _, a := range accounts {
		k := string(a.Provider) + "\x00" + a.Home
		if !seen[k] {
			seen[k] = true
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider == usage.Claude
		}
		li, lj := strings.ToLower(out[i].Label), strings.ToLower(out[j].Label)
		if li != lj {
			return li < lj
		}
		return out[i].Home < out[j].Home
	})
	return out
}

func usageAccountForSession(inst *session.Instance) (usage.Account, error) {
	if inst == nil {
		return usage.Account{}, fmt.Errorf("session is required")
	}
	if session.IsClaudeCompatible(inst.Tool) {
		return usage.Account{Provider: usage.Claude, Home: usage.CanonicalHome(session.GetClaudeConfigDirForInstance(inst)), Label: inst.Title}, nil
	}
	if session.IsCodexCompatible(inst.Tool) {
		return usage.Account{Provider: usage.Codex, Home: usage.CanonicalHome(session.GetCodexConfigDirForInstance(inst)), Label: inst.Title}, nil
	}
	return usage.Account{}, fmt.Errorf("usage is unsupported for %s sessions", inst.Tool)
}

func handleUsage(profile string, args []string) {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	all := fs.Bool("all", false, "query all configured local accounts")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck usage --all [--json]")
		fmt.Fprintln(fs.Output(), "Usage: agent-deck usage <session> [--json]")
	}
	if err := fs.Parse(args); err != nil {
		if err != flag.ErrHelp {
			os.Exit(2)
		}
		return
	}
	if *all && fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage --all does not accept a session target")
		os.Exit(2)
	}
	if !*all && fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage accepts one session target")
		os.Exit(2)
	}
	config, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage: load config: %v\n", err)
		os.Exit(1)
	}
	accounts := configuredUsageAccounts(config)
	if !*all {
		storage, storageErr := session.NewStorageWithProfile(profile)
		if storageErr != nil {
			fmt.Fprintf(os.Stderr, "usage: initialize storage: %v\n", storageErr)
			os.Exit(1)
		}
		instances, _, loadErr := storage.LoadWithGroups()
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "usage: load sessions: %v\n", loadErr)
			os.Exit(1)
		}
		inst, errMsg, _ := ResolveSessionOrCurrent(fs.Arg(0), instances)
		if inst == nil {
			fmt.Fprintf(os.Stderr, "usage: %s (provide a session target or use --all)\n", errMsg)
			os.Exit(2)
		}
		account, accountErr := usageAccountForSession(inst)
		if accountErr != nil {
			fmt.Fprintf(os.Stderr, "usage: %v\n", accountErr)
			os.Exit(1)
		}
		accounts = []usage.Account{account}
	}
	results := make([]usage.Snapshot, 0, len(accounts))
	runner := usage.Runner{}
	for _, account := range accounts {
		s, err := runner.Query(context.Background(), account)
		if err != nil {
			results = append(results, usage.Snapshot{Provider: account.Provider, Account: account.Label, Error: err.Error()})
			continue
		}
		results = append(results, s)
	}
	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(results)
		return
	}
	for _, s := range results {
		if s.Error != "" {
			fmt.Printf("%s %s: unavailable (%s)\n", s.Provider, s.Account, s.Error)
			continue
		}
		parts := []string{fmt.Sprintf("%s %s", strings.Title(string(s.Provider)), s.Account)}
		if s.Windows.Session5H != nil {
			parts = append(parts, fmt.Sprintf("5h %d%% left", s.Windows.Session5H.RemainingPercent))
		}
		if s.Windows.Weekly != nil {
			parts = append(parts, fmt.Sprintf("weekly %d%% left", s.Windows.Weekly.RemainingPercent))
		}
		fmt.Println(strings.Join(parts, " "))
	}
}
