package costs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// RawCostEvent is the JSON structure written by hook_handler.
type RawCostEvent struct {
	InstanceID         string   `json:"instance_id"`
	Provider           string   `json:"provider"`
	SourceKind         string   `json:"source_kind"`
	SourceIdentity     string   `json:"source_identity"`
	SourceAliases      []string `json:"source_aliases,omitempty"`
	TranscriptIdentity string   `json:"transcript_identity"`
	Model              string   `json:"model"`
	InputTokens        int64    `json:"input_tokens"`
	OutputTokens       int64    `json:"output_tokens"`
	CacheReadTokens    int64    `json:"cache_read_tokens"`
	CacheWriteTokens   int64    `json:"cache_write_tokens"`
	CacheWrite5mTokens int64    `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64    `json:"cache_write_1h_tokens"`
	ReasoningTokens    int64    `json:"reasoning_tokens"`
	Timestamp          int64    `json:"ts"`
}

// CostEventWatcher watches a directory for new cost event JSON files.
type CostEventWatcher struct {
	dir     string
	watcher *fsnotify.Watcher
	eventCh chan RawCostEvent
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewCostEventWatcher creates a watcher for the given directory.
func NewCostEventWatcher(dir string) (*CostEventWatcher, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	if err := w.Add(dir); err != nil {
		w.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &CostEventWatcher{
		dir:     dir,
		watcher: w,
		eventCh: make(chan RawCostEvent, 64),
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// EventCh returns the channel that emits parsed cost events.
func (w *CostEventWatcher) EventCh() <-chan RawCostEvent {
	return w.eventCh
}

// Start begins watching for file events. Blocks until stopped.
func (w *CostEventWatcher) Start() {
	defer close(w.eventCh)
	var mu sync.Mutex
	pending := make(map[string]struct{})
	var timer *time.Timer

	processPending := func() {
		mu.Lock()
		files := make([]string, 0, len(pending))
		for f := range pending {
			files = append(files, f)
		}
		pending = make(map[string]struct{})
		mu.Unlock()

		for _, f := range files {
			w.processFile(f)
		}
	}

	for {
		select {
		case <-w.ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			if filepath.Ext(event.Name) != ".json" || strings.HasSuffix(event.Name, ".tmp") {
				continue
			}
			mu.Lock()
			pending[event.Name] = struct{}{}
			mu.Unlock()

			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(100*time.Millisecond, processPending)
		case <-w.watcher.Errors:
			// continue
		}
	}
}

// Stop cancels the watcher and closes resources.
func (w *CostEventWatcher) Stop() {
	w.cancel()
	w.watcher.Close()
}

func (w *CostEventWatcher) processFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var ev RawCostEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		os.Remove(path) // malformed, remove
		return
	}

	select {
	case w.eventCh <- ev:
		os.Remove(path) // only delete after successful send
	default:
		// channel full, leave file for retry on next fsnotify event
	}
}

// WriteRawCostEvent persists one event emitted by the supported hook watcher.
func (s *Store) WriteRawCostEvent(raw RawCostEvent, pricer *Pricer) error {
	if raw.Provider != "" && raw.SourceIdentity != "" {
		usage := TokenUsage{
			InputTokens: raw.InputTokens, OutputTokens: raw.OutputTokens,
			CacheReadTokens: raw.CacheReadTokens, CacheWriteTokens: raw.CacheWriteTokens,
			CacheWrite5mTokens: raw.CacheWrite5mTokens, CacheWrite1hTokens: raw.CacheWrite1hTokens,
			ReasoningTokens: raw.ReasoningTokens,
		}
		event := UsageEvent{
			ID:       raw.InstanceID + "_" + fmt.Sprintf("%d", raw.Timestamp),
			Provider: raw.Provider, SourceKind: raw.SourceKind,
			SourceIdentity: raw.SourceIdentity, SourceAliases: raw.SourceAliases,
			TranscriptIdentity: raw.TranscriptIdentity, SessionID: raw.InstanceID,
			Timestamp: time.Unix(0, raw.Timestamp).UTC(), Model: raw.Model, Usage: usage,
			PricingStatus: PricingUnknown, ReconciliationStatus: ReconciliationAuthoritative,
		}
		if pricer != nil {
			applyEventPrice(&event, pricer)
		}
		_, err := s.Ingest(context.Background(), []UsageEvent{event}, nil)
		return err
	}
	event := CostEvent{
		ID:               raw.InstanceID + "_" + fmt.Sprintf("%d", raw.Timestamp),
		SessionID:        raw.InstanceID,
		Timestamp:        time.Unix(0, raw.Timestamp),
		Model:            raw.Model,
		InputTokens:      raw.InputTokens,
		OutputTokens:     raw.OutputTokens,
		CacheReadTokens:  raw.CacheReadTokens,
		CacheWriteTokens: raw.CacheWriteTokens,
	}
	if pricer != nil {
		event.CostMicrodollars = pricer.ComputeCost(raw.Model, raw.InputTokens, raw.OutputTokens, raw.CacheReadTokens, raw.CacheWriteTokens)
	}
	return s.WriteCostEvent(event)
}
