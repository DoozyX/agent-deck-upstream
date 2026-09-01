package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageRejectsNewGroupFromManagedSessionBeforeInstanceWrite(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, ".local", "share"))
	configDir := filepath.Join(root, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("[group_defaults]\nmanual_creation_only = true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	t.Setenv("AGENTDECK_INSTANCE_ID", "managed-session")

	storage, err := NewStorageWithProfile("manual-creation-storage-test")
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	inst := &Instance{
		ID:          "blocked-instance",
		Title:       "blocked",
		ProjectPath: filepath.Join(root, "project"),
		GroupPath:   "agent-made",
		Tool:        "shell",
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
	}
	err = storage.SaveWithGroups([]*Instance{inst}, NewGroupTree([]*Instance{inst}))
	if err == nil || !strings.Contains(err.Error(), "group creation is restricted to the user") {
		t.Fatalf("SaveWithGroups error = %v, want manual-creation restriction", err)
	}

	exists, err := storage.InstanceExists(inst.ID)
	if err != nil {
		t.Fatalf("check instance: %v", err)
	}
	if exists {
		t.Fatal("denied save wrote the instance before rejecting its group")
	}

	err = storage.InsertSessionAndVerify(inst, nil)
	if err == nil || !strings.Contains(err.Error(), "group creation is restricted to the user") {
		t.Fatalf("InsertSessionAndVerify error = %v, want manual-creation restriction", err)
	}
	exists, err = storage.InstanceExists(inst.ID)
	if err != nil {
		t.Fatalf("check targeted insert: %v", err)
	}
	if exists {
		t.Fatal("denied targeted insert wrote the instance before rejecting its group")
	}

	trustedTree := NewGroupTree(nil)
	trustedTree.CreateGroup("user-made")
	if err := storage.SaveGroupsOnly(trustedTree); err != nil {
		t.Fatalf("trusted declarative group save: %v", err)
	}
	allowed := &Instance{
		ID:          "allowed-instance",
		Title:       "allowed",
		ProjectPath: filepath.Join(root, "allowed"),
		GroupPath:   "user-made",
		Tool:        "shell",
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{allowed}, NewGroupTreeWithGroups(
		[]*Instance{allowed}, []*GroupData{{Name: "user-made", Path: "user-made"}})); err != nil {
		t.Fatalf("managed session could not use existing group: %v", err)
	}
}
