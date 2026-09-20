package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSkillsPackages implements `agent-deck skills-packages refresh`.
func handleSkillsPackages(profile string, args []string) {
	usage := func() {
		fmt.Println("Usage: agent-deck skills-packages refresh [--group <path>] [--json]")
		fmt.Println()
		fmt.Println("Install or update [skills_packages.*] into git default_path dirs of groups")
		fmt.Println("that attach them. Same core as the background refresher.")
	}
	if len(args) == 0 || helpRequested(args[:1]) {
		usage()
		if len(args) == 0 {
			os.Exit(1)
		}
		return
	}
	if args[0] != "refresh" {
		fmt.Fprintf(os.Stderr, "unknown skills-packages command %q\n", args[0])
		usage()
		os.Exit(1)
	}

	fs := flag.NewFlagSet("skills-packages refresh", flag.ExitOnError)
	group := fs.String("group", "", "Only refresh this group path and its subgroups")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		usage()
		fmt.Println()
		fmt.Println("Options:")
		fs.SetOutput(os.Stdout)
		fs.PrintDefaults()
	}
	if helpRequested(args[1:]) {
		fs.Usage()
		return
	}
	if err := fs.Parse(normalizeArgs(fs, args[1:])); err != nil {
		os.Exit(1)
	}

	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: config.toml: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	groups := session.SkillsGroupsFor(cfg, profile)
	results := session.RunSkillsPackagesRefresh(ctx, cfg, session.SkillsRefreshOptions{
		Groups:      groups,
		GroupFilter: *group,
	})

	failed := 0
	for _, r := range results {
		if r.Status == session.SkillsStatusFailed {
			failed++
		}
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]interface{}{"success": failed == 0, "results": results})
	} else if len(results) == 0 {
		fmt.Println("No install targets: attach a package with [groups.<path>].skills_packages and set a default_path.")
	} else {
		for _, r := range results {
			line := fmt.Sprintf("%-8s %s  %s  [%s]", r.Status, r.Package, r.Dir, r.Group)
			if r.Action != "" {
				line += "  (" + r.Action + ")"
			}
			if r.Reason != "" {
				line += "  " + strings.ReplaceAll(r.Reason, "\n", " ")
			}
			fmt.Println(line)
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}
