package main

// `agent-deck artifacts sync <remote|user@host>` — cross-machine run-artifact
// union over the EXISTING registered-remote ssh path.
//
// The gap: run artifacts live at <main-worktree>/.agent-deck/<run-id>/ and
// .agent-deck/handoff/<session-id>/. docs/data-locations.md makes them project
// artifacts on purpose, and ensureRepositoryGitExclude keeps them out of git on
// purpose — so a run recorded on one machine is invisible on every other one.
// `remote drain` already pulls completion RECORDS across hosts; nothing moves
// FILES, and the manual rsync that stands in for this is neither portable
// (macOS ships rsync 2.6.9) nor able to say what it did.
//
// Why a union and not a mirror: measured across two machines on 2026-08-31,
// 3608 shared artifact files were byte-identical, 187 existed only on one side
// and 7 only on the other. Divergence is real in both directions and almost
// purely additive, so the correct operation adds what is missing and removes
// nothing. A same-path/different-hash pair is therefore not a merge to
// automate but a CONFLICT to report: it moves in neither direction and the
// command exits non-zero so a script cannot mistake it for a clean sync.

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// artifactEntry is one syncable file in a run-artifact tree. Path is
// slash-separated and relative to <root>/.agent-deck, so it is comparable
// across machines whose absolute roots differ (~doozyx vs ~skletnikov).
type artifactEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

// artifactPlan is what one root's sync will do. The four buckets are
// exhaustive and mutually exclusive: every path present on either side lands
// in exactly one of them.
type artifactPlan struct {
	Pull      []string
	Push      []string
	Identical int
	Conflicts []string
}

// diffManifests classifies every path present on either side. Sorted output so
// a report and a --json payload are stable across runs and across the map
// iteration order that produced them.
func diffManifests(local, remote []artifactEntry) artifactPlan {
	remoteByPath := make(map[string]artifactEntry, len(remote))
	for _, e := range remote {
		remoteByPath[e.Path] = e
	}
	var plan artifactPlan
	for _, l := range local {
		r, onRemote := remoteByPath[l.Path]
		switch {
		case !onRemote:
			plan.Push = append(plan.Push, l.Path)
		case r.Sha256 == l.Sha256:
			plan.Identical++
		default:
			plan.Conflicts = append(plan.Conflicts, l.Path)
		}
	}
	localPaths := make(map[string]struct{}, len(local))
	for _, e := range local {
		localPaths[e.Path] = struct{}{}
	}
	for _, r := range remote {
		if _, onLocal := localPaths[r.Path]; !onLocal {
			plan.Pull = append(plan.Pull, r.Path)
		}
	}
	sort.Strings(plan.Pull)
	sort.Strings(plan.Push)
	sort.Strings(plan.Conflicts)
	return plan
}

// artifactsTmpDirName is the one top-level directory under .agent-deck that is
// per-session scratch (a session's TMPDIR) rather than a run artifact.
const artifactsTmpDirName = "tmp"

// scanArtifacts lists the syncable files under <root>/.agent-deck.
//
// The rule is "every top-level directory except tmp/", which is what makes the
// set self-maintaining: <run-id>/ and handoff/ are in, tmp/ is out, and the
// top-level FILES that live beside them — skills.toml, worktree-setup.sh,
// worktree-destruction.sh — are out because they are per-checkout config the
// user authored for this machine, not a record of a run.
//
// A missing .agent-deck is not an error. --all hands over every root in the
// registry and most of them have never hosted a run.
//
// Symlinks are skipped rather than followed or recreated: a link's target is a
// path on the machine that made it, and reproducing it on another host would
// either dangle or point somewhere unintended.
func scanArtifacts(root string) ([]artifactEntry, error) {
	base := filepath.Join(root, session.ProjectDataDirName)
	top, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", base, err)
	}
	var entries []artifactEntry
	for _, dir := range top {
		if !dir.IsDir() || dir.Name() == artifactsTmpDirName {
			continue
		}
		walkRoot := filepath.Join(base, dir.Name())
		err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			sum, size, err := hashFile(path)
			if err != nil {
				return err
			}
			entries = append(entries, artifactEntry{
				Path:   filepath.ToSlash(rel),
				Size:   size,
				Sha256: sum,
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", walkRoot, err)
		}
	}
	return entries, nil
}

func hashFile(path string) (sum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// safeArtifactRel validates one slash-separated member/request path and returns
// the local path it may be written to or read from.
//
// Both directions need this and neither may trust its input: an unpack member
// name arrives in a tar from another host, and a pack request path arrives in
// that host's manifest. The rules are the D5 scope restated as a gate — a
// single relative path, inside .agent-deck, under a top-level directory that is
// not tmp/ — which also happens to reject every path-traversal spelling,
// because "../x" and "/etc/x" have no top-level directory inside the tree.
func safeArtifactRel(root, member string) (string, error) {
	if member == "" || strings.ContainsRune(member, 0) {
		return "", fmt.Errorf("artifact path %q: empty or contains NUL", member)
	}
	if path.IsAbs(member) || strings.HasPrefix(member, "/") || filepath.IsAbs(member) {
		return "", fmt.Errorf("artifact path %q: absolute", member)
	}
	clean := path.Clean(member)
	if clean != member {
		return "", fmt.Errorf("artifact path %q: not in canonical form (%q)", member, clean)
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("artifact path %q: not under a run directory", member)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("artifact path %q: contains %q", member, part)
		}
	}
	if parts[0] == artifactsTmpDirName {
		return "", fmt.Errorf("artifact path %q: %s/ is per-session scratch, not a run artifact", member, artifactsTmpDirName)
	}
	base := filepath.Join(root, session.ProjectDataDirName)
	full := filepath.Join(base, filepath.FromSlash(clean))
	// Belt and braces: the rules above already exclude an escape, so a full
	// path that is not under base means one of them is wrong.
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("artifact path %q: resolves outside %s", member, base)
	}
	return full, nil
}

// packArtifacts streams the named files as a tar. Paths are validated before
// any byte is read, so a malformed request produces an error instead of a
// partial stream the far side would try to extract.
func packArtifacts(root string, paths []string, out io.Writer) error {
	full := make([]string, 0, len(paths))
	for _, p := range paths {
		resolved, err := safeArtifactRel(root, p)
		if err != nil {
			return err
		}
		full = append(full, resolved)
	}
	tw := tar.NewWriter(out)
	for i, p := range paths {
		if err := packOne(tw, full[i], p); err != nil {
			return err
		}
	}
	return tw.Close()
}

func packOne(tw *tar.Writer, full, member string) error {
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact %q: not a regular file", member)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:     member,
		Mode:     0o644,
		Size:     info.Size(),
		ModTime:  info.ModTime(),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// unpackArtifacts extracts a tar into <root>/.agent-deck and returns how many
// files it created.
//
// It never overwrites: a member whose destination already exists is skipped and
// not counted. That is what makes the transfer safe to repeat and safe to race
// against the far side — a path that appeared between manifest and transfer
// degrades to "already there", never to a clobber. It also never deletes.
//
// Each file lands through a temp file in the destination directory and
// O_EXCL rename-in-place, so an interrupted transfer cannot leave a truncated
// artifact behind under a real name.
func unpackArtifacts(root string, in io.Reader) (int, error) {
	tr := tar.NewReader(in)
	written := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return written, nil
		}
		if err != nil {
			return written, err
		}
		if hdr.Typeflag != tar.TypeReg {
			// Directories are implied by their members; anything else
			// (symlink, device, hardlink) is out of scope by design.
			continue
		}
		dest, err := safeArtifactRel(root, hdr.Name)
		if err != nil {
			return written, err
		}
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return written, err
		}
		if err := writeNewFile(dest, tr); err != nil {
			return written, err
		}
		written++
	}
}

func writeNewFile(dest string, src io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".artifacts-sync-*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

const (
	artifactsExitUsage       = 2
	artifactsExitUnreachable = 3
	artifactsExitConflict    = 4
)

var errUnknownArtifactRemote = errors.New("unknown remote")

type artifactRemote interface {
	Home(ctx context.Context) (string, error)
	Manifest(ctx context.Context, root string) ([]artifactEntry, bool, error)
	Pull(ctx context.Context, root string, paths []string, dst io.Writer) error
	Push(ctx context.Context, root string, src io.Reader) (int, error)
}

// artifactRepoResult is one root's outcome. Skipped is a human reason, never
// empty when the root was not synced — a skip that reads as a clean sync is the
// failure mode D7 exists to prevent.
type artifactRepoResult struct {
	Root       string   `json:"root"`
	RemoteRoot string   `json:"remote_root,omitempty"`
	Skipped    string   `json:"skipped,omitempty"`
	Pulled     int      `json:"pulled,omitempty"`
	Pushed     int      `json:"pushed,omitempty"`
	Identical  int      `json:"identical,omitempty"`
	Conflicts  []string `json:"conflicts,omitempty"`
}

type artifactSyncReport struct {
	Remote    string               `json:"remote"`
	Repos     []artifactRepoResult `json:"repos"`
	Pulled    int                  `json:"pulled"`
	Pushed    int                  `json:"pushed"`
	Conflicts int                  `json:"conflicts"`
	DryRun    bool                 `json:"dry_run"`
}

func runArtifactsSync(stdout, stderr io.Writer, args []string, cwd string, open func(string) (artifactRemote, error)) int {
	flags := flag.NewFlagSet("artifacts sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "plan and report without transferring")
	all := flags.Bool("all", false, "every project root in the session registry, not just this one")
	asJSON := flags.Bool("json", false, "emit a machine-readable result")
	// normalizeArgs so `sync m1 --dry-run` works: Go's flag package stops at the
	// first non-flag argument, and the remote name is the natural first word.
	if err := flags.Parse(normalizeArgs(flags, args)); err != nil {
		return artifactsExitUsage
	}
	rest := flags.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: agent-deck artifacts sync <remote|user@host> [--dry-run] [--json]")
		return artifactsExitUsage
	}
	name := rest[0]

	remote, err := open(name)
	if err != nil {
		fmt.Fprintf(stderr, "artifacts sync: remote %q: %v\n", name, err)
		return artifactsExitUsage
	}

	ctx := context.Background()
	remoteHome, err := remote.Home(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "artifacts sync: could not reach %s: %v\n", name, err)
		return artifactsExitUnreachable
	}
	localHome, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "artifacts sync: local home: %v\n", err)
		return 1
	}

	report := artifactSyncReport{Remote: name, DryRun: *dryRun}
	exit := 0
	roots, err := artifactSyncRoots(cwd, *all)
	if err != nil {
		fmt.Fprintf(stderr, "artifacts sync: %v\n", err)
		return 1
	}
	for _, root := range roots {
		result, code := syncOneRoot(ctx, remote, root, localHome, remoteHome, *dryRun)
		report.Repos = append(report.Repos, result)
		report.Pulled += result.Pulled
		report.Pushed += result.Pushed
		report.Conflicts += len(result.Conflicts)
		if code != 0 && exit == 0 {
			exit = code
		}
	}
	if report.Conflicts > 0 && exit == 0 {
		exit = artifactsExitConflict
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "artifacts sync: %v\n", err)
			return 1
		}
		return exit
	}
	printArtifactSyncReport(stdout, report)
	return exit
}

// artifactRegistryRoots lists the working directory of every session the
// registry knows. A package-level seam so the --all fan-out is provable without
// a populated state.db.
var artifactRegistryRoots = func() ([]string, error) {
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		return nil, fmt.Errorf("open session registry: %w", err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadLite()
	if err != nil {
		return nil, fmt.Errorf("read session registry: %w", err)
	}
	roots := make([]string, 0, len(instances))
	for _, inst := range instances {
		if inst != nil && inst.ProjectPath != "" {
			roots = append(roots, inst.ProjectPath)
		}
	}
	return roots, nil
}

// artifactSyncRoots resolves which project roots this invocation covers.
//
// Default is the one the caller is standing in. Every candidate is folded to
// its main worktree, so a session inside .worktrees/ syncs the same tree the
// main checkout does — and so two sessions in two worktrees of one repo collapse
// to a single root instead of syncing it twice.
//
// Without --all the registry is never consulted: syncing every repository you
// have ever opened, because you happened to be standing in one of them, is not
// what the command was asked to do.
func artifactSyncRoots(cwd string, all bool) ([]string, error) {
	candidates := []string{cwd}
	if all {
		registry, err := artifactRegistryRoots()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, registry...)
	}
	seen := make(map[string]struct{}, len(candidates))
	roots := make([]string, 0, len(candidates))
	for i, c := range candidates {
		// The registry outlives the filesystem — it still names worktrees that
		// have since been deleted. Such a root has nothing to sync and no main
		// worktree to fold into, so it would only add a skip line. The cwd
		// (i == 0) is kept regardless: the caller asked about it by standing
		// there, and a bad path deserves the error, not silence.
		if i > 0 {
			if _, err := os.Stat(c); err != nil {
				continue
			}
		}
		root := c
		if folded, err := git.GetMainWorktreePath(c); err == nil && folded != "" {
			root = folded
		}
		if _, dup := seen[root]; dup {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots, nil
}

func syncOneRoot(ctx context.Context, remote artifactRemote, root, localHome, remoteHome string, dryRun bool) (artifactRepoResult, int) {
	result := artifactRepoResult{Root: root}

	rel, ok := artifactHomeRelative(root, localHome)
	if !ok {
		result.Skipped = fmt.Sprintf("%s is not under %s, so it cannot be mapped onto the remote", root, localHome)
		return result, 0
	}
	remoteRoot := filepath.Join(remoteHome, filepath.FromSlash(rel))
	result.RemoteRoot = remoteRoot

	remoteEntries, present, err := remote.Manifest(ctx, remoteRoot)
	if err != nil {
		result.Skipped = fmt.Sprintf("remote manifest failed: %v", err)
		return result, artifactsExitUnreachable
	}
	if !present {
		result.Skipped = fmt.Sprintf("%s not present on the remote", remoteRoot)
		return result, 0
	}
	localEntries, err := scanArtifacts(root)
	if err != nil {
		result.Skipped = fmt.Sprintf("local scan failed: %v", err)
		return result, 1
	}

	plan := diffManifests(localEntries, remoteEntries)
	result.Identical = plan.Identical
	result.Conflicts = plan.Conflicts
	if dryRun {
		result.Pulled = len(plan.Pull)
		result.Pushed = len(plan.Push)
		return result, 0
	}

	if len(plan.Pull) > 0 {
		n, err := pullArtifacts(ctx, remote, remoteRoot, root, plan.Pull)
		result.Pulled = n
		if err != nil {
			result.Skipped = fmt.Sprintf("pull failed after %d files: %v", n, err)
			return result, artifactsExitUnreachable
		}
	}
	if len(plan.Push) > 0 {
		n, err := pushArtifacts(ctx, remote, remoteRoot, root, plan.Push)
		result.Pushed = n
		if err != nil {
			result.Skipped = fmt.Sprintf("push failed after %d files: %v", n, err)
			return result, artifactsExitUnreachable
		}
	}
	return result, 0
}

// artifactHomeRelative expresses root relative to home. D6: the two machines
// differ only in their home directory (~doozyx vs ~skletnikov), so a
// home-relative path is the mapping. A root outside home has no mapping and
// must not get a guessed one.
func artifactHomeRelative(root, home string) (string, bool) {
	if home == "" {
		return "", false
	}
	rel, err := filepath.Rel(home, root)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// pullArtifacts streams the remote's tar straight into the local tree. A pipe
// rather than a buffer because one leg measured 127 MB in real use.
func pullArtifacts(ctx context.Context, remote artifactRemote, remoteRoot, localRoot string, paths []string) (int, error) {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(remote.Pull(ctx, remoteRoot, paths, pw))
	}()
	written, err := unpackArtifacts(localRoot, pr)
	// Unblocks the packer if extraction stopped early (a rejected member); the
	// pipe's own Close never errors.
	_ = pr.CloseWithError(err)
	return written, err
}

func pushArtifacts(ctx context.Context, remote artifactRemote, remoteRoot, localRoot string, paths []string) (int, error) {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(packArtifacts(localRoot, paths, pw))
	}()
	written, err := remote.Push(ctx, remoteRoot, pr)
	_ = pr.CloseWithError(err)
	return written, err
}

func printArtifactSyncReport(out io.Writer, report artifactSyncReport) {
	transferred := 0
	for _, r := range report.Repos {
		label := filepath.Base(r.Root)
		if r.Skipped != "" {
			fmt.Fprintf(out, "%-24s skipped: %s\n", label, r.Skipped)
			continue
		}
		transferred += r.Pulled + r.Pushed
		suffix := ""
		if r.Pulled == 0 && r.Pushed == 0 && len(r.Conflicts) == 0 {
			suffix = " (already in sync)"
		}
		fmt.Fprintf(out, "%-24s <- %-5d -> %-5d = %d identical%s\n",
			label, r.Pulled, r.Pushed, r.Identical, suffix)
		for _, c := range r.Conflicts {
			fmt.Fprintf(out, "%-24s CONFLICT (differs on both sides, moved neither way): %s\n", "", c)
		}
	}
	verb := "transferred"
	if report.DryRun {
		verb = "to transfer (--dry-run)"
	}
	fmt.Fprintf(out, "%d roots, %d %s, %d conflicts\n",
		len(report.Repos), transferred, verb, report.Conflicts)
}

type artifactManifestPayload struct {
	Present bool            `json:"present"`
	Entries []artifactEntry `json:"entries"`
}

// runArtifacts dispatches the verb. sync is the human entry point; manifest,
// pack and unpack are the primitives the far side runs over ssh, and they are
// plain local operations on purpose — the transport carries no logic, so the
// same code that a sync exercises locally is what runs remotely.
func runArtifacts(stdout, stderr io.Writer, stdin io.Reader, args []string) int {
	if len(args) == 0 {
		printArtifactsUsage(stderr)
		return artifactsExitUsage
	}
	switch args[0] {
	case "sync":
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "artifacts: %v\n", err)
			return 1
		}
		return runArtifactsSync(stdout, stderr, args[1:], cwd, openArtifactRemote)
	case "home":
		// The far side's $HOME, for the home-relative root remap (D6). Asking
		// the remote's own agent-deck keeps it on the same path as every other
		// remote call, including its shell quoting and host validation.
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "artifacts home: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, home)
		return 0
	case "manifest":
		return runArtifactsManifest(stdout, stderr, args[1:])
	case "pack":
		return runArtifactsPack(stdout, stderr, stdin, args[1:])
	case "unpack":
		return runArtifactsUnpack(stdout, stderr, stdin, args[1:])
	case "help", "--help", "-h":
		printArtifactsUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "Unknown artifacts command: %s\n", args[0])
		printArtifactsUsage(stderr)
		return artifactsExitUsage
	}
}

func artifactsRootFlag(name string, stderr io.Writer, args []string) (string, bool) {
	flags := flag.NewFlagSet("artifacts "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "project root whose .agent-deck tree to operate on")
	if err := flags.Parse(normalizeArgs(flags, args)); err != nil {
		return "", false
	}
	if *root == "" || !filepath.IsAbs(*root) {
		fmt.Fprintf(stderr, "artifacts %s: --root must be an absolute path\n", name)
		return "", false
	}
	return *root, true
}

// runArtifactsManifest answers with present=false rather than an error when the
// root is not there. The caller uses that to SKIP the root; an error would be
// indistinguishable from ssh failing, and a skip must never be reported as a
// clean sync (D7).
func runArtifactsManifest(stdout, stderr io.Writer, args []string) int {
	root, ok := artifactsRootFlag("manifest", stderr, args)
	if !ok {
		return artifactsExitUsage
	}
	payload := artifactManifestPayload{}
	if _, err := os.Stat(root); err == nil {
		payload.Present = true
		entries, err := scanArtifacts(root)
		if err != nil {
			fmt.Fprintf(stderr, "artifacts manifest: %v\n", err)
			return 1
		}
		payload.Entries = entries
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "artifacts manifest: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(payload); err != nil {
		fmt.Fprintf(stderr, "artifacts manifest: %v\n", err)
		return 1
	}
	return 0
}

func runArtifactsPack(stdout, stderr io.Writer, stdin io.Reader, args []string) int {
	root, ok := artifactsRootFlag("pack", stderr, args)
	if !ok {
		return artifactsExitUsage
	}
	if stdin == nil {
		fmt.Fprintln(stderr, "artifacts pack: no file list on stdin")
		return artifactsExitUsage
	}
	var paths []string
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			paths = append(paths, line)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(stderr, "artifacts pack: read file list: %v\n", err)
		return 1
	}
	if err := packArtifacts(root, paths, stdout); err != nil {
		fmt.Fprintf(stderr, "artifacts pack: %v\n", err)
		return 1
	}
	return 0
}

func runArtifactsUnpack(stdout, stderr io.Writer, stdin io.Reader, args []string) int {
	root, ok := artifactsRootFlag("unpack", stderr, args)
	if !ok {
		return artifactsExitUsage
	}
	if stdin == nil {
		fmt.Fprintln(stderr, "artifacts unpack: no tar stream on stdin")
		return artifactsExitUsage
	}
	written, err := unpackArtifacts(root, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "artifacts unpack: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%d\n", written)
	return 0
}

func printArtifactsUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage: agent-deck artifacts <command> [options]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Sync run artifacts (.agent-deck/<run-id>/, handoff/) between machines.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  sync <remote|user@host>   Union this machine's run artifacts with a remote's")
	fmt.Fprintln(out, "                            (pull what is missing here, push what is missing there,")
	fmt.Fprintln(out, "                            never delete, never overwrite)")
	fmt.Fprintln(out, "  manifest --root <path>    Print this machine's artifact manifest (read-only)")
	fmt.Fprintln(out, "  pack --root <path>        Tar the paths listed on stdin to stdout")
	fmt.Fprintln(out, "  unpack --root <path>      Extract a tar from stdin; skips paths that exist")
	fmt.Fprintln(out, "  home                      Print this machine's home directory")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Sync options:")
	fmt.Fprintln(out, "  --all       Every project root in the session registry, not just this one")
	fmt.Fprintln(out, "  --dry-run   Report the plan without transferring")
	fmt.Fprintln(out, "  --json      Machine-readable result")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Exit codes: 0 synced, 2 usage, 3 remote unreachable, 4 conflicts found")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Examples:")
	fmt.Fprintln(out, "  agent-deck artifacts sync m1")
	fmt.Fprintln(out, "  agent-deck artifacts sync m1 --all --dry-run")
}
