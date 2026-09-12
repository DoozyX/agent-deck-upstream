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
	dir      string
	watcher  *fsnotify.Watcher
	eventCh  chan *CostEventDelivery
	retryCh  chan string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	inFlight map[string]bool
}

// CostEventDelivery remains backed by its durable queue file until Ack.
type CostEventDelivery struct {
	RawCostEvent
	watcher *CostEventWatcher
	path    string
	once    sync.Once
}

// Ack removes a queue file only after its event has been persisted.
func (d *CostEventDelivery) Ack() error {
	var err error
	d.once.Do(func() {
		err = os.Remove(d.path)
		if os.IsNotExist(err) {
			err = nil
		}
		d.watcher.release(d.path)
		if err != nil {
			d.watcher.retry(d.path)
		}
	})
	return err
}

// Retry releases a failed delivery and schedules the durable file again.
func (d *CostEventDelivery) Retry() {
	d.once.Do(func() {
		d.watcher.release(d.path)
		d.watcher.retry(d.path)
	})
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
		dir:      dir,
		watcher:  w,
		eventCh:  make(chan *CostEventDelivery, 64),
		retryCh:  make(chan string, 64),
		ctx:      ctx,
		cancel:   cancel,
		inFlight: make(map[string]bool),
	}, nil
}

// EventCh returns the channel that emits parsed cost events.
func (w *CostEventWatcher) EventCh() <-chan *CostEventDelivery {
	return w.eventCh
}

// Start begins watching for file events. Blocks until stopped.
func (w *CostEventWatcher) Start() {
	defer close(w.eventCh)
	var mu sync.Mutex
	pending := make(map[string]struct{})
	var timer *time.Timer
	var timerCh <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	w.scanExisting()

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
		case path := <-w.retryCh:
			w.processFile(path)
		case <-timerCh:
			processPending()
			timerCh = nil
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

			if timer == nil {
				timer = time.NewTimer(100 * time.Millisecond)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(100 * time.Millisecond)
			}
			timerCh = timer.C
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
	if !w.claim(path) {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		w.release(path)
		return
	}
	var ev RawCostEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		_ = os.Remove(path) // malformed queue payloads cannot be retried
		w.release(path)
		return
	}

	select {
	case w.eventCh <- &CostEventDelivery{RawCostEvent: ev, watcher: w, path: path}:
	default:
		w.release(path)
		w.retry(path)
	}
}

func (w *CostEventWatcher) claim(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inFlight[path] {
		return false
	}
	w.inFlight[path] = true
	return true
}

func (w *CostEventWatcher) release(path string) {
	w.mu.Lock()
	delete(w.inFlight, path)
	w.mu.Unlock()
}

func (w *CostEventWatcher) retry(path string) {
	time.AfterFunc(time.Second, func() {
		select {
		case <-w.ctx.Done():
			return
		case w.retryCh <- path:
		}
	})
}

func (w *CostEventWatcher) scanExisting() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" || strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		w.processFile(filepath.Join(w.dir, entry.Name()))
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
		_, err := s.IngestPriced(context.Background(), []UsageEvent{event}, nil, pricer)
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
