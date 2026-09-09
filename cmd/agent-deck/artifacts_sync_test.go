package main

// `agent-deck artifacts sync <remote>` — cross-machine run-artifact union.
//
// What these pin:
//   - the four buckets are exhaustive and mutually exclusive: a path is pulled,
//     pushed, counted identical, or reported as a conflict — never two of those;
//   - a same-path/different-hash pair is a CONFLICT and moves in neither
//     direction, because artifacts are append-only and a divergence is a signal
//     for a human, not a merge to automate.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestArtifactsDiff_FourBuckets(t *testing.T) {
	local := []artifactEntry{
		{Path: "run-a/orchestrate/report.md", Size: 10, Sha256: "same"},
		{Path: "run-a/orchestrate/local-only.md", Size: 3, Sha256: "l"},
		{Path: "run-b/design/design.md", Size: 7, Sha256: "local-version"},
	}
	remote := []artifactEntry{
		{Path: "run-a/orchestrate/report.md", Size: 10, Sha256: "same"},
		{Path: "run-c/orchestrate/remote-only.md", Size: 4, Sha256: "r"},
		{Path: "run-b/design/design.md", Size: 9, Sha256: "remote-version"},
	}

	plan := diffManifests(local, remote)

	if got, want := plan.Push, []string{"run-a/orchestrate/local-only.md"}; !equalStrings(got, want) {
		t.Errorf("push = %v, want %v", got, want)
	}
	if got, want := plan.Pull, []string{"run-c/orchestrate/remote-only.md"}; !equalStrings(got, want) {
		t.Errorf("pull = %v, want %v", got, want)
	}
	if plan.Identical != 1 {
		t.Errorf("identical = %d, want 1", plan.Identical)
	}
	if got, want := plan.Conflicts, []string{"run-b/design/design.md"}; !equalStrings(got, want) {
		t.Errorf("conflicts = %v, want %v", got, want)
	}
}

// A conflicting path must not also be scheduled for transfer: shipping it in
// either direction is the overwrite the design forbids.
func TestArtifactsDiff_ConflictMovesInNeitherDirection(t *testing.T) {
	local := []artifactEntry{{Path: "run/x.md", Size: 1, Sha256: "a"}}
	remote := []artifactEntry{{Path: "run/x.md", Size: 1, Sha256: "b"}}

	plan := diffManifests(local, remote)

	if len(plan.Pull) != 0 || len(plan.Push) != 0 {
		t.Fatalf("conflict scheduled for transfer: pull=%v push=%v", plan.Pull, plan.Push)
	}
	if plan.Identical != 0 {
		t.Errorf("identical = %d, want 0 (hashes differ)", plan.Identical)
	}
	if len(plan.Conflicts) != 1 {
		t.Fatalf("conflicts = %v, want 1 entry", plan.Conflicts)
	}
}

// D5: the syncable set is <run-id>/** and handoff/**. tmp/ and skills.toml are
// per-checkout by design (docs/data-locations.md) — syncing a session's TMPDIR
// or another checkout's skill attachments across machines is not "the same
// artifact in two places", it is corruption of machine-local state.
// writeArtifact puts one file into a root's .agent-deck tree, creating parents.
func writeArtifact(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, ".agent-deck", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactsScan_ExcludesPerCheckoutState(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) { writeArtifact(t, root, rel, body) }
	write("2026-08-31-a-run/design/design.md", "spec")
	write("2026-08-31-a-run/orchestrate/findings.md", "found")
	write("handoff/abc123-1787/PROMPT.md", "prompt")
	write("tmp/sess-1/node-compile-cache", "scratch")
	write("skills.toml", "[skills]")

	entries, err := scanArtifacts(root)
	if err != nil {
		t.Fatalf("scanArtifacts: %v", err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Path)
	}
	sort.Strings(got)
	want := []string{
		"2026-08-31-a-run/design/design.md",
		"2026-08-31-a-run/orchestrate/findings.md",
		"handoff/abc123-1787/PROMPT.md",
	}
	if !equalStrings(got, want) {
		t.Errorf("scanned = %v, want %v", got, want)
	}
}

// A hash that does not depend on the file's bytes cannot detect a conflict, and
// a size that is not the real size makes the report a lie.
func TestArtifactsScan_HashesContent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agent-deck", "run", "orchestrate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "r.md"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := scanArtifacts(root)
	if err != nil {
		t.Fatalf("scanArtifacts: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want 1", entries)
	}
	// sha256("abc")
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if entries[0].Sha256 != want {
		t.Errorf("sha256 = %q, want %q", entries[0].Sha256, want)
	}
	if entries[0].Size != 3 {
		t.Errorf("size = %d, want 3", entries[0].Size)
	}
}

// A root with no .agent-deck tree is not an error: it is a repo that has never
// hosted a run, and --all will hand over plenty of them.
func TestArtifactsScan_MissingTreeIsEmptyNotError(t *testing.T) {
	entries, err := scanArtifacts(t.TempDir())
	if err != nil {
		t.Fatalf("scanArtifacts on repo with no .agent-deck: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %v, want none", entries)
	}
}

// tarOf builds a tar stream with literal member names, including names a
// well-behaved packer would never emit. The point is that unpackArtifacts must
// not trust the stream: it arrives from another machine over ssh.
func tarOf(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := members[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// D9: a tar from another host must not be able to place a file outside the
// target .agent-deck tree. Each of these is a real escape technique.
func TestArtifactsUnpack_RejectsEscapingMembers(t *testing.T) {
	for _, member := range []string{
		"../../../../etc/evil.md",
		"/etc/evil.md",
		"run/../../escape.md",
		"tmp/sess/scratch.md",
		"skills.toml",
	} {
		t.Run(member, func(t *testing.T) {
			root := t.TempDir()
			written, err := unpackArtifacts(root, bytes.NewReader(tarOf(t, map[string]string{member: "evil"})))
			if err == nil {
				t.Fatalf("member %q accepted (wrote %d)", member, written)
			}
			if written != 0 {
				t.Errorf("member %q rejected but %d files written", member, written)
			}
			// Nothing may exist outside the root either.
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escape.md")); statErr == nil {
				t.Errorf("member %q escaped the root", member)
			}
		})
	}
}

// "Never overwrite" is the whole safety story of a union sync. A path that
// appeared between manifest and transfer must degrade to "already there".
func TestArtifactsUnpack_NeverOverwrites(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, ".agent-deck", "run", "orchestrate", "r.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	written, err := unpackArtifacts(root, bytes.NewReader(tarOf(t, map[string]string{
		"run/orchestrate/r.md":   "incoming",
		"run/orchestrate/new.md": "fresh",
	})))
	if err != nil {
		t.Fatalf("unpackArtifacts: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "original" {
		t.Errorf("existing file overwritten: %q", got)
	}
	if written != 1 {
		t.Errorf("written = %d, want 1 (the new file only)", written)
	}
	if got, _ := os.ReadFile(filepath.Join(root, ".agent-deck", "run", "orchestrate", "new.md")); string(got) != "fresh" {
		t.Errorf("new file = %q, want %q", got, "fresh")
	}
}

// pack -> unpack must reproduce the bytes; that round trip is the whole point.
func TestArtifactsPackUnpack_RoundTrip(t *testing.T) {
	src := t.TempDir()
	body := strings.Repeat("evidence\n", 5000)
	full := filepath.Join(src, ".agent-deck", "2026-08-31-run", "orchestrate", "log.txt")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var stream bytes.Buffer
	if err := packArtifacts(src, []string{"2026-08-31-run/orchestrate/log.txt"}, &stream); err != nil {
		t.Fatalf("packArtifacts: %v", err)
	}
	dst := t.TempDir()
	written, err := unpackArtifacts(dst, &stream)
	if err != nil {
		t.Fatalf("unpackArtifacts: %v", err)
	}
	if written != 1 {
		t.Fatalf("written = %d, want 1", written)
	}
	got, err := os.ReadFile(filepath.Join(dst, ".agent-deck", "2026-08-31-run", "orchestrate", "log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("round trip corrupted %d bytes -> %d bytes", len(body), len(got))
	}
}

// packArtifacts must not let a caller-supplied path list read outside the tree
// either: the list comes from the far side's manifest.
func TestArtifactsPack_RejectsEscapingRequest(t *testing.T) {
	src := t.TempDir()
	if err := packArtifacts(src, []string{"../../../../etc/passwd"}, io.Discard); err == nil {
		t.Fatal("pack accepted an escaping request path")
	}
}

// fakeArtifactRemote is a real .agent-deck tree in a temp dir, driven through
// the real scan/pack/unpack code. It stands in for ssh, not for the behavior
// under test: a transfer that "succeeds" here really moved bytes.
type fakeArtifactRemote struct {
	home        string
	unreachable error
	packCalls   int
	pushCalls   int
}

func (f *fakeArtifactRemote) Home(context.Context) (string, error) {
	if f.unreachable != nil {
		return "", f.unreachable
	}
	return f.home, nil
}

func (f *fakeArtifactRemote) Manifest(_ context.Context, root string) ([]artifactEntry, bool, error) {
	if f.unreachable != nil {
		return nil, false, f.unreachable
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	entries, err := scanArtifacts(root)
	return entries, true, err
}

func (f *fakeArtifactRemote) Pull(_ context.Context, root string, paths []string, dst io.Writer) error {
	f.packCalls++
	return packArtifacts(root, paths, dst)
}

func (f *fakeArtifactRemote) Push(_ context.Context, root string, src io.Reader) (int, error) {
	f.pushCalls++
	return unpackArtifacts(root, src)
}

// syncFixture builds two machines that differ the way M5 and M1 actually did:
// a file only here, a file only there, and one identical on both.
func syncFixture(t *testing.T) (localHome, remoteHome, localRoot, remoteRoot string, remote *fakeArtifactRemote) {
	t.Helper()
	base := t.TempDir()
	localHome = filepath.Join(base, "home-local")
	remoteHome = filepath.Join(base, "home-remote")
	localRoot = filepath.Join(localHome, "DoozyX", "Uniqcast", "gss")
	remoteRoot = filepath.Join(remoteHome, "DoozyX", "Uniqcast", "gss")
	put := func(root, rel, body string) { writeArtifact(t, root, rel, body) }
	put(localRoot, "run-shared/orchestrate/report.md", "same bytes")
	put(remoteRoot, "run-shared/orchestrate/report.md", "same bytes")
	put(localRoot, "run-local/design/design.md", "local only")
	put(remoteRoot, "run-remote/orchestrate/verdicts.md", "remote only")
	t.Setenv("HOME", localHome)
	return localHome, remoteHome, localRoot, remoteRoot, &fakeArtifactRemote{home: remoteHome}
}

func TestArtifactsSync_MovesBothDirections(t *testing.T) {
	_, _, localRoot, remoteRoot, remote := syncFixture(t)
	var out, errOut bytes.Buffer

	code := runArtifactsSync(&out, &errOut, []string{"m1"}, localRoot, stubOpen(remote))

	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s%s", code, out.String(), errOut.String())
	}
	pulled := filepath.Join(localRoot, ".agent-deck", "run-remote", "orchestrate", "verdicts.md")
	if got, err := os.ReadFile(pulled); err != nil || string(got) != "remote only" {
		t.Errorf("pulled file = %q, err %v", got, err)
	}
	pushed := filepath.Join(remoteRoot, ".agent-deck", "run-local", "design", "design.md")
	if got, err := os.ReadFile(pushed); err != nil || string(got) != "local only" {
		t.Errorf("pushed file = %q, err %v", got, err)
	}
	shared := filepath.Join(localRoot, ".agent-deck", "run-shared", "orchestrate", "report.md")
	if got, _ := os.ReadFile(shared); string(got) != "same bytes" {
		t.Errorf("identical file disturbed: %q", got)
	}
}

// Running it twice must be a no-op that says so, not a second transfer.
func TestArtifactsSync_SecondRunIsConvergedAndSaysSo(t *testing.T) {
	_, _, localRoot, _, remote := syncFixture(t)
	var out bytes.Buffer
	if code := runArtifactsSync(&out, &out, []string{"m1"}, localRoot, stubOpen(remote)); code != 0 {
		t.Fatalf("first run exit = %d", code)
	}
	out.Reset()
	code := runArtifactsSync(&out, &out, []string{"m1"}, localRoot, stubOpen(remote))
	if code != 0 {
		t.Fatalf("second run exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "already in sync") {
		t.Errorf("converged run did not say so:\n%s", out.String())
	}
}

// --dry-run must plan and report without moving a byte.
func TestArtifactsSync_DryRunTransfersNothing(t *testing.T) {
	_, _, localRoot, remoteRoot, remote := syncFixture(t)
	var out bytes.Buffer

	if code := runArtifactsSync(&out, &out, []string{"--dry-run", "m1"}, localRoot, stubOpen(remote)); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(localRoot, ".agent-deck", "run-remote")); !errors.Is(err, os.ErrNotExist) {
		t.Error("--dry-run pulled a file")
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, ".agent-deck", "run-local")); !errors.Is(err, os.ErrNotExist) {
		t.Error("--dry-run pushed a file")
	}
	if remote.packCalls != 0 || remote.pushCalls != 0 {
		t.Errorf("--dry-run opened transfers: pack=%d push=%d", remote.packCalls, remote.pushCalls)
	}
	if !strings.Contains(out.String(), "1") {
		t.Errorf("--dry-run reported no plan:\n%s", out.String())
	}
}

// A same-path/different-hash pair exits 4 and names the path — and still
// transfers everything that is not in conflict.
func TestArtifactsSync_ConflictExitsFourAndStillSyncsTheRest(t *testing.T) {
	_, _, localRoot, remoteRoot, remote := syncFixture(t)
	for root, body := range map[string]string{localRoot: "ours", remoteRoot: "theirs"} {
		full := filepath.Join(root, ".agent-deck", "run-shared", "orchestrate", "diverged.md")
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer

	code := runArtifactsSync(&out, &out, []string{"m1"}, localRoot, stubOpen(remote))

	if code != artifactsExitConflict {
		t.Fatalf("exit = %d, want %d\n%s", code, artifactsExitConflict, out.String())
	}
	if !strings.Contains(out.String(), "diverged.md") {
		t.Errorf("conflict not named:\n%s", out.String())
	}
	if got, _ := os.ReadFile(filepath.Join(localRoot, ".agent-deck", "run-shared", "orchestrate", "diverged.md")); string(got) != "ours" {
		t.Errorf("conflicting file was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(localRoot, ".agent-deck", "run-remote", "orchestrate", "verdicts.md")); err != nil {
		t.Errorf("non-conflicting pull was abandoned: %v", err)
	}
}

// "I could not ask" must never look like "there was nothing to sync".
func TestArtifactsSync_UnreachableIsNotEmpty(t *testing.T) {
	_, _, localRoot, _, remote := syncFixture(t)
	remote.unreachable = errors.New("ssh: connect: host is down")
	var out bytes.Buffer

	code := runArtifactsSync(&out, &out, []string{"m1"}, localRoot, stubOpen(remote))

	if code != artifactsExitUnreachable {
		t.Fatalf("exit = %d, want %d\n%s", code, artifactsExitUnreachable, out.String())
	}
	if strings.Contains(out.String(), "already in sync") {
		t.Errorf("unreachable remote reported as converged:\n%s", out.String())
	}
}

func TestArtifactsSync_UnknownRemoteExitsTwo(t *testing.T) {
	_, _, localRoot, _, _ := syncFixture(t)
	var out bytes.Buffer
	open := func(string) (artifactRemote, error) { return nil, errUnknownArtifactRemote }

	code := runArtifactsSync(&out, &out, []string{"nosuchhost"}, localRoot, open)

	if code != artifactsExitUsage {
		t.Fatalf("exit = %d, want %d\n%s", code, artifactsExitUsage, out.String())
	}
	if !strings.Contains(out.String(), "nosuchhost") {
		t.Errorf("message does not name the remote:\n%s", out.String())
	}
}

// D7: a root the remote does not have is SKIPPED with a reason. Silently
// treating it as an empty remote tree would push the whole local history into a
// path that is not that repo on the far side.
func TestArtifactsSync_AbsentRemoteRootIsSkippedWithReason(t *testing.T) {
	_, remoteHome, localRoot, remoteRoot, remote := syncFixture(t)
	if err := os.RemoveAll(filepath.Join(remoteHome, "DoozyX")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	code := runArtifactsSync(&out, &out, []string{"m1"}, localRoot, stubOpen(remote))

	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a skip is not a failure)\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "skipped") {
		t.Errorf("skip not reported:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, ".agent-deck")); err == nil {
		t.Error("pushed into an absent remote root")
	}
}

func stubOpen(r artifactRemote) func(string) (artifactRemote, error) {
	return func(string) (artifactRemote, error) { return r, nil }
}

// R6: --all covers every project root that has hosted a run, not just the one
// the caller happens to stand in. Six repos under one tree is the real case.
func TestArtifactsSync_AllCoversEveryRoot(t *testing.T) {
	_, remoteHome, localRoot, _, remote := syncFixture(t)
	secondLocal := filepath.Join(filepath.Dir(localRoot), "dispatcher")
	secondRemote := filepath.Join(remoteHome, "DoozyX", "Uniqcast", "dispatcher")
	put := func(root, rel, body string) { writeArtifact(t, root, rel, body) }
	put(secondRemote, "run-d/orchestrate/only-there.md", "second repo")
	put(secondLocal, "run-d/orchestrate/here.md", "second repo local")

	oldRoots := artifactRegistryRoots
	artifactRegistryRoots = func() ([]string, error) { return []string{localRoot, secondLocal}, nil }
	t.Cleanup(func() { artifactRegistryRoots = oldRoots })

	var out bytes.Buffer
	if code := runArtifactsSync(&out, &out, []string{"--all", "m1"}, localRoot, stubOpen(remote)); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(secondLocal, ".agent-deck", "run-d", "orchestrate", "only-there.md")); err != nil {
		t.Errorf("--all did not sync the second root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localRoot, ".agent-deck", "run-remote", "orchestrate", "verdicts.md")); err != nil {
		t.Errorf("--all skipped the cwd root: %v", err)
	}
	if !strings.Contains(out.String(), "2 roots") {
		t.Errorf("report did not cover 2 roots:\n%s", out.String())
	}
}

// Without --all, the registry is not consulted at all: syncing every repo you
// have ever opened because you were standing in one of them is a surprise.
func TestArtifactsSync_WithoutAllIgnoresRegistry(t *testing.T) {
	_, _, localRoot, _, remote := syncFixture(t)
	oldRoots := artifactRegistryRoots
	consulted := false
	artifactRegistryRoots = func() ([]string, error) {
		consulted = true
		return nil, nil
	}
	t.Cleanup(func() { artifactRegistryRoots = oldRoots })

	var out bytes.Buffer
	if code := runArtifactsSync(&out, &out, []string{"m1"}, localRoot, stubOpen(remote)); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if consulted {
		t.Error("registry consulted without --all")
	}
}

// The remote side of a sync is three local operations invoked over ssh. They
// must work standalone, because that is exactly how the far side runs them.
func TestArtifactsManifestSubcommand_EmitsScannedTree(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, ".agent-deck", "run", "orchestrate", "r.md")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := runArtifacts(&out, &errOut, nil, []string{"manifest", "--root", root}); code != 0 {
		t.Fatalf("exit = %d: %s", code, errOut.String())
	}
	var got artifactManifestPayload
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, out.String())
	}
	if !got.Present {
		t.Error("present = false for a root that exists")
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "run/orchestrate/r.md" {
		t.Errorf("entries = %+v", got.Entries)
	}
}

// "The root is not here" must be a successful answer, not an error: it is how
// D7's skip is decided, and an error would be indistinguishable from ssh dying.
func TestArtifactsManifestSubcommand_AbsentRootIsPresentFalse(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runArtifacts(&out, &errOut, nil, []string{"manifest", "--root", filepath.Join(t.TempDir(), "nope")})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, errOut.String())
	}
	var got artifactManifestPayload
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Present {
		t.Error("present = true for a root that does not exist")
	}
}

func TestArtifactsPackUnpackSubcommands_RoundTripOverStdio(t *testing.T) {
	src := t.TempDir()
	full := filepath.Join(src, ".agent-deck", "run", "orchestrate", "r.md")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	var tarStream, errOut bytes.Buffer
	code := runArtifacts(&tarStream, &errOut, strings.NewReader("run/orchestrate/r.md\n"),
		[]string{"pack", "--root", src})
	if code != 0 {
		t.Fatalf("pack exit = %d: %s", code, errOut.String())
	}

	dst := t.TempDir()
	var unpackOut bytes.Buffer
	code = runArtifacts(&unpackOut, &errOut, &tarStream, []string{"unpack", "--root", dst})
	if code != 0 {
		t.Fatalf("unpack exit = %d: %s", code, errOut.String())
	}
	got, err := os.ReadFile(filepath.Join(dst, ".agent-deck", "run", "orchestrate", "r.md"))
	if err != nil || string(got) != "payload" {
		t.Errorf("round trip = %q, err %v", got, err)
	}
	if !strings.Contains(unpackOut.String(), "1") {
		t.Errorf("unpack did not report its count: %q", unpackOut.String())
	}
}

// An escaping member must fail the subcommand, not just the library call: this
// is the process the far side actually invokes.
func TestArtifactsUnpackSubcommand_RejectsEscape(t *testing.T) {
	var out, errOut bytes.Buffer
	code := runArtifacts(&out, &errOut, bytes.NewReader(tarOf(t, map[string]string{"../../evil.md": "x"})),
		[]string{"unpack", "--root", t.TempDir()})
	if code == 0 {
		t.Fatal("unpack subcommand accepted an escaping member")
	}
}

func TestArtifacts_UnknownSubcommandIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runArtifacts(&out, &errOut, nil, []string{"frobnicate"}); code != artifactsExitUsage {
		t.Fatalf("exit = %d, want %d", code, artifactsExitUsage)
	}
}

// The home remap (D6) needs the far side's $HOME, and asking the remote's own
// agent-deck for it keeps the answer on the same code path as everything else.
func TestArtifactsHomeSubcommand_PrintsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out, errOut bytes.Buffer
	if code := runArtifacts(&out, &errOut, nil, []string{"home"}); code != 0 {
		t.Fatalf("exit = %d: %s", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != home {
		t.Errorf("home = %q, want %q", strings.TrimSpace(out.String()), home)
	}
}

// `artifacts sync m1 --dry-run` is how a person actually types it: the remote
// is the subject, the flags are an afterthought. Go's flag package stops
// parsing at the first non-flag argument, so without normalization --dry-run
// arrives as a second positional and the command reports a usage error while
// silently NOT being a dry run.
func TestArtifactsSync_FlagsAfterTheRemoteName(t *testing.T) {
	_, _, localRoot, remoteRoot, remote := syncFixture(t)
	var out bytes.Buffer

	code := runArtifactsSync(&out, &out, []string{"m1", "--dry-run"}, localRoot, stubOpen(remote))

	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, ".agent-deck", "run-local")); !errors.Is(err, os.ErrNotExist) {
		t.Error("trailing --dry-run was ignored: a file was pushed")
	}
	if remote.packCalls != 0 || remote.pushCalls != 0 {
		t.Errorf("trailing --dry-run was ignored: pack=%d push=%d", remote.packCalls, remote.pushCalls)
	}
}

// The registry outlives the filesystem: it still names worktrees that have
// since been deleted. Those roots have no artifacts to sync and no main
// worktree to fold into, so carrying them into the report buries the roots that
// do matter under skip lines. Observed live: 48 roots, most of them stale.
func TestArtifactsSync_AllDropsRootsThatNoLongerExist(t *testing.T) {
	_, _, localRoot, _, remote := syncFixture(t)
	gone := filepath.Join(filepath.Dir(localRoot), ".worktrees", "deleted-long-ago")

	oldRoots := artifactRegistryRoots
	artifactRegistryRoots = func() ([]string, error) { return []string{localRoot, gone}, nil }
	t.Cleanup(func() { artifactRegistryRoots = oldRoots })

	var out bytes.Buffer
	if code := runArtifactsSync(&out, &out, []string{"--all", "m1"}, localRoot, stubOpen(remote)); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "deleted-long-ago") {
		t.Errorf("a root that does not exist reached the report:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "1 roots") {
		t.Errorf("want 1 root after dropping the stale one:\n%s", out.String())
	}
}
