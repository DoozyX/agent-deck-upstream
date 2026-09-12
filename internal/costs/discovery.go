package costs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const UnassignedSessionID = "unassigned"

type ProviderHome struct {
	Provider string
	Path     string
	Account  string
}

type TranscriptAttribution struct {
	Provider        string
	Home            string
	NativeSessionID string
	SessionID       string
	ParentSessionID string
	RunID           string
	Archived        bool
}

type DiscoveryConfig struct {
	Homes        []ProviderHome
	Attributions []TranscriptAttribution
}

type CoverageWarning struct {
	Provider string `json:"provider"`
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

var codexSessionIDSuffix = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

func DiscoverTranscriptSources(config DiscoveryConfig) ([]TranscriptSource, []CoverageWarning, error) {
	attributions := make(map[string]TranscriptAttribution, len(config.Attributions))
	for _, attribution := range config.Attributions {
		home, err := canonicalPath(attribution.Home)
		if err != nil {
			return nil, nil, fmt.Errorf("canonicalize %s attribution home: %w", attribution.Provider, err)
		}
		attribution.Home = home
		attributions[attributionKey(attribution.Provider, home, attribution.NativeSessionID)] = attribution
	}

	seenHomes := make(map[string]bool)
	seenFiles := make(map[string]bool)
	var sources []TranscriptSource
	var warnings []CoverageWarning
	for _, configuredHome := range config.Homes {
		home, err := canonicalPath(configuredHome.Path)
		if err != nil {
			if os.IsNotExist(err) {
				warnings = append(warnings, CoverageWarning{Provider: configuredHome.Provider, Source: configuredHome.Path, Kind: "home_missing", Message: "configured provider home does not exist"})
				continue
			}
			return nil, warnings, fmt.Errorf("canonicalize %s home %q: %w", configuredHome.Provider, configuredHome.Path, err)
		}
		homeKey := configuredHome.Provider + "\x00" + home
		if seenHomes[homeKey] {
			continue
		}
		seenHomes[homeKey] = true

		root := ""
		switch configuredHome.Provider {
		case ProviderClaude:
			root = filepath.Join(home, "projects")
		case ProviderCodex:
			root = filepath.Join(home, "sessions")
		default:
			warnings = append(warnings, CoverageWarning{Provider: configuredHome.Provider, Source: home, Kind: "provider_unsupported", Message: "provider has no transcript discovery route"})
			continue
		}
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, warnings, fmt.Errorf("read %s transcript root %q: %w", configuredHome.Provider, root, err)
		}
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
				return nil
			}
			canonicalFile, err := canonicalPath(path)
			if err != nil {
				return err
			}
			fileKey := configuredHome.Provider + "\x00" + canonicalFile
			if seenFiles[fileKey] {
				return nil
			}
			seenFiles[fileKey] = true
			source, ok := discoveredSource(configuredHome.Provider, home, configuredHome.Account, root, canonicalFile, attributions)
			if ok {
				sources = append(sources, source)
			}
			return nil
		})
		if err != nil {
			return nil, warnings, fmt.Errorf("walk %s transcripts in %q: %w", configuredHome.Provider, root, err)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	return sources, warnings, nil
}

func discoveredSource(provider, home, account, root, path string, attributions map[string]TranscriptAttribution) (TranscriptSource, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	nativeID := base
	kind := ""
	identity := ""
	parentNativeID := ""
	switch provider {
	case ProviderClaude:
		kind = SourceKindClaudeDirect
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return TranscriptSource{}, false
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		for i, part := range parts {
			if part == "subagents" && i > 0 {
				parentNativeID = parts[i-1]
				kind = SourceKindClaudeNativeChild
				identity = "claude:" + parentNativeID + "/subagents/" + base
				break
			}
		}
		if identity == "" {
			identity = "claude:" + nativeID
		}
	case ProviderCodex:
		kind = SourceKindCodexRollout
		match := codexSessionIDSuffix.FindStringSubmatch(filepath.Base(path))
		if len(match) != 2 {
			return TranscriptSource{}, false
		}
		nativeID = match[1]
		identity = "codex:" + nativeID
	default:
		return TranscriptSource{}, false
	}

	lookupID := nativeID
	if parentNativeID != "" {
		lookupID = parentNativeID
	}
	attribution, found := attributions[attributionKey(provider, home, lookupID)]
	source := TranscriptSource{
		Provider: provider, Kind: kind, Identity: identity, Path: path, Account: account,
		SessionID: UnassignedSessionID,
	}
	if found {
		source.SessionID = attribution.SessionID
		source.ParentSessionID = attribution.ParentSessionID
		source.RunID = attribution.RunID
	}
	return source, true
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func attributionKey(provider, home, nativeID string) string {
	return provider + "\x00" + home + "\x00" + nativeID
}
