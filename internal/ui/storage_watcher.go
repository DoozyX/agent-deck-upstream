package ui

import (
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

var watcherLog = logging.ForComponent(logging.CompStorage)

const pollInterval = 2 * time.Second

// StorageWatcher observes committed material registry changes on a dedicated
// live SQLite connection. Own writes are conservatively reloaded too.
type StorageWatcher struct {
	observer       *statedb.RegistryObserver
	reloadCh       chan struct{}
	closeCh        chan struct{}
	closeOnce      sync.Once
	mu             sync.Mutex
	acknowledged   *statedb.RegistrySnapshotResult
	archived       bool
	scannedVersion int64
	scannedEpoch   uint64
	pending        bool
	sequence       uint64
	closed         bool
}

type storageLoadTicket struct {
	sequence                uint64
	archived                bool
	before, after           int64
	beforeEpoch, afterEpoch uint64
}

func NewStorageWatcher(db *statedb.StateDB) (*StorageWatcher, error) {
	if db == nil {
		return nil, nil
	}
	observer, err := db.NewRegistryObserver()
	if err != nil {
		return nil, err
	}
	snapshot, _, after, err := observer.SnapshotByArchive(false)
	if err != nil {
		_ = observer.Close()
		return nil, err
	}
	return &StorageWatcher{observer: observer, reloadCh: make(chan struct{}, 1), closeCh: make(chan struct{}), acknowledged: snapshot, scannedVersion: after, pending: true}, nil
}
func (sw *StorageWatcher) Start() { go sw.pollLoop() }
func (sw *StorageWatcher) pollLoop() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sw.closeCh:
			return
		case <-ticker.C:
			sw.checkAndNotify()
		}
	}
}
func (sw *StorageWatcher) signalLocked() {
	if sw.closed {
		return
	}
	select {
	case sw.reloadCh <- struct{}{}:
	default:
	}
}
func (sw *StorageWatcher) checkAndNotify() {
	version, epoch, err := sw.observer.Probe()
	if err != nil {
		watcherLog.Debug("watcher_poll_failed", slog.String("error", err.Error()))
		sw.mu.Lock()
		sw.pending = true
		sw.signalLocked()
		sw.mu.Unlock()
		return
	}
	sw.mu.Lock()
	unchanged := version == sw.scannedVersion && epoch == sw.scannedEpoch && !sw.pending
	closed := sw.closed
	sw.mu.Unlock()
	if unchanged || closed {
		return
	}
	sw.mu.Lock()
	archived := sw.archived
	sw.mu.Unlock()
	snapshot, _, after, err := sw.observer.SnapshotByArchive(archived)
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.closed {
		return
	}
	// A view switch can race this poll. Its load command owns the new
	// acknowledged snapshot, so discard the old-scope scan rather than
	// comparing two different archive partitions.
	if archived != sw.archived {
		return
	}
	if err != nil {
		sw.pending = true
		sw.signalLocked()
		return
	}
	sw.scannedVersion = after
	sw.scannedEpoch = epoch
	if !registrySnapshotsMateriallyEqual(snapshot, sw.acknowledged) {
		// The notification means the next UI load is expected to apply this
		// snapshot. Make it the comparison baseline now so acknowledgment only
		// reports edits that happen while that load is in flight.
		sw.acknowledged = filterWatcherSnapshot(snapshot, archived)
		sw.pending = true
	}
	if sw.pending {
		sw.signalLocked()
	}
}

func (sw *StorageWatcher) ReloadChannel() <-chan struct{} { return sw.reloadCh }

// NotifySave remains compatible with callers. Intent is not commit provenance;
// it never suppresses unrelated writes.
func (sw *StorageWatcher) NotifySave() {}

// NotifyStatusWrite advances the observer's acknowledged volatile status for a
// write made by this process. Status writes happen on every live sweep and are
// already reflected in memory; acknowledging them here prevents those writes
// from masquerading as full registry edits. A concurrent material edit still
// differs from the snapshot and causes a reload.
func (sw *StorageWatcher) NotifyStatusWrite(id, status, tool string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.closed || sw.acknowledged == nil {
		return
	}
	for _, row := range sw.acknowledged.Instances {
		if row != nil && row.ID == id {
			row.Status = status
			row.Tool = tool
			return
		}
	}
}

func (sw *StorageWatcher) TriggerReload() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.pending = true
	sw.signalLocked()
}
func (sw *StorageWatcher) issueLoad() storageLoadTicket {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.sequence++
	return storageLoadTicket{sequence: sw.sequence, archived: sw.archived}
}

func (sw *StorageWatcher) beginLoad() (storageLoadTicket, error) {
	return sw.probeLoad(sw.issueLoad())
}

func (sw *StorageWatcher) probeLoad(ticket storageLoadTicket) (storageLoadTicket, error) {
	version, epoch, err := sw.observer.Probe()
	ticket.beforeEpoch = epoch
	ticket.before = version
	return ticket, err
}
func (sw *StorageWatcher) endLoad(ticket storageLoadTicket) (storageLoadTicket, error) {
	version, epoch, err := sw.observer.Probe()
	ticket.afterEpoch = epoch
	ticket.after = version
	return ticket, err
}
func (sw *StorageWatcher) current(ticket storageLoadTicket) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return !sw.closed && ticket.sequence == sw.sequence
}
func (sw *StorageWatcher) acknowledge(ticket storageLoadTicket, snapshot *statedb.RegistrySnapshotResult, success bool) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.closed || ticket.sequence != sw.sequence {
		return
	}
	if !success || snapshot == nil {
		sw.pending = true
		sw.signalLocked()
		return
	}
	if ticket.archived != sw.archived {
		// The user switched views while this load was in flight. Do not let an
		// obsolete partition replace the watcher baseline.
		sw.pending = true
		sw.signalLocked()
		return
	}
	filtered := filterWatcherSnapshot(snapshot, ticket.archived)
	materialChanged := sw.acknowledged != nil && !registrySnapshotsMateriallyEqual(filtered, sw.acknowledged)
	sw.acknowledged = filtered
	// Keep the scan cursor at the load's boundary so the next poll performs one
	// material comparison. This catches edits made during the load while
	// avoiding a follow-up reload for volatile status-only writes.
	boundaryChanged := ticket.beforeEpoch != ticket.afterEpoch ||
		sw.scannedEpoch != ticket.beforeEpoch
	sw.pending = materialChanged || boundaryChanged
	sw.scannedVersion = ticket.before
	sw.scannedEpoch = ticket.beforeEpoch
	if sw.pending {
		sw.signalLocked()
	}
}

// SetArchiveView changes the partition observed by future loads and polls.
// The following load establishes the new baseline; no synthetic notification
// is emitted because the filter change already schedules that load directly.
func (sw *StorageWatcher) SetArchiveView(archived bool) {
	sw.mu.Lock()
	if sw.archived != archived {
		// The two partitions are independent baselines. The view's next load
		// establishes the new one; comparing it with the previous partition
		// would manufacture a reload after every view switch.
		sw.acknowledged = nil
	}
	sw.archived = archived
	sw.mu.Unlock()
}

func filterWatcherSnapshot(snapshot *statedb.RegistrySnapshotResult, archived bool) *statedb.RegistrySnapshotResult {
	if snapshot == nil {
		return nil
	}
	filtered := &statedb.RegistrySnapshotResult{Groups: snapshot.Groups}
	var instances []*statedb.InstanceRow
	for _, row := range snapshot.Instances {
		if row == nil {
			continue
		}
		if row.ArchivedAt.IsZero() != archived {
			if instances == nil {
				instances = make([]*statedb.InstanceRow, 0, len(snapshot.Instances))
			}
			instances = append(instances, statedb.CloneInstanceRow(row))
		}
	}
	filtered.Instances = instances
	return filtered
}

// registrySnapshotsMateriallyEqual compares the fields that require
// rehydrating the TUI. WriteStatus intentionally updates only the volatile
// status/tool pair (plus the separate acknowledgment column), and those
// values are refreshed by the background status path. Treating them as a
// registry edit makes concurrent agent-deck processes turn every status tick
// into a full session reload, which starves scroll input on a busy profile.
func registrySnapshotsMateriallyEqual(a, b *statedb.RegistrySnapshotResult) bool {
	if a == nil || b == nil {
		return a == b
	}
	if !reflect.DeepEqual(a.Groups, b.Groups) || len(a.Instances) != len(b.Instances) {
		return false
	}
	for i, left := range a.Instances {
		right := b.Instances[i]
		if left == nil || right == nil {
			if left != right {
				return false
			}
			continue
		}
		leftCopy := *left
		rightCopy := *right
		leftCopy.Status = ""
		rightCopy.Status = ""
		leftCopy.Tool = ""
		rightCopy.Tool = ""
		if !reflect.DeepEqual(leftCopy, rightCopy) {
			return false
		}
	}
	return true
}

func (sw *StorageWatcher) Warning() string { return "" }
func (sw *StorageWatcher) Close() error {
	var err error
	sw.closeOnce.Do(func() { sw.mu.Lock(); sw.closed = true; close(sw.closeCh); sw.mu.Unlock(); err = sw.observer.Close() })
	return err
}
