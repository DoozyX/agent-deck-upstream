package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestListJSONIncludeGroupsIncludesEmptySavedGroup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	const profile = "list_include_groups"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	tree := session.NewGroupTreeWithGroups(nil, []*session.GroupData{{
		Name: "empty",
		Path: "work/empty",
	}})
	if err := storage.SaveWithGroups(nil, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestListJSONIncludeGroupsHelper$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_LIST_INCLUDE_GROUPS_HELPER=1",
		"AGENT_DECK_LIST_INCLUDE_GROUPS_PROFILE="+profile,
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list --json --include-groups failed: %v", err)
	}

	var snapshot struct {
		Sessions []json.RawMessage   `json:"sessions"`
		Groups   []session.GroupData `json:"groups"`
	}
	if err := json.Unmarshal(out, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v\noutput: %s", err, out)
	}
	if snapshot.Sessions == nil {
		t.Fatal("sessions must be an empty JSON array, not null")
	}
	for _, group := range snapshot.Groups {
		if group.Path == "work/empty" {
			return
		}
	}
	t.Fatalf("empty saved group missing from snapshot: %+v", snapshot.Groups)
}

func TestListJSONIncludeGroupsHelper(t *testing.T) {
	if os.Getenv("AGENT_DECK_LIST_INCLUDE_GROUPS_HELPER") != "1" {
		return
	}
	handleList(os.Getenv("AGENT_DECK_LIST_INCLUDE_GROUPS_PROFILE"), []string{"--json", "--include-groups"})
	os.Exit(0)
}
