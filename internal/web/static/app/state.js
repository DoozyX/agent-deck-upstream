// state.js -- Shared signals for vanilla JS <-> Preact bridge
// Vanilla JS imports these and sets .value on SSE updates.
// Preact components import these and read .value reactively.
import { signal } from '@preact/signals'
import { apiFetch } from './api.js'

// Session data from SSE snapshot
export const sessionsSignal = signal([])

// Archived sessions from GET /api/sessions/archived
export const archivedSessionsSignal = signal([])

// Currently selected session ID
export const selectedIdSignal = signal(null)

// Currently selected remote session: null, or { remote: 'm5', session: {id,title,tool,status,path} }.
// Mutually exclusive with selectedIdSignal — use selectLocalSession /
// selectRemoteSession below rather than setting either signal directly.
export const selectedRemoteSignal = signal(null)

export function selectLocalSession(id) {
  selectedRemoteSignal.value = null
  selectedIdSignal.value = id
}

// remoteTerminalKey is the single source of truth for a remote attachment's
// identity: TerminalPanel keys its terminal + WebSocket effect on it, and
// remoteAttachFailedSignal below names the exact attempt that failed.
export function remoteTerminalKey(remote, sessionId, attempt) {
  return `remote:${remote}:${sessionId}#${attempt || 0}`
}

// remoteAttachFailedSignal holds the remoteTerminalKey of the remote attachment
// that ended in a fatal REMOTE_ATTACH_FAILED, or null. TerminalPanel sets it
// from the fatal frame and clears it whenever it starts a fresh attach, so at
// most one key — always the currently mounted one — is ever marked.
export const remoteAttachFailedSignal = signal(null)

// `attempt` makes re-clicking a FAILED remote tile a retry gesture instead of a
// no-op (round 7 #1). TerminalPanel keys its terminal + WebSocket effect on
// `remote:<name>:<id>#<attempt>`, which does not change on a re-click, and the
// pane is only ever CSS-hidden, never unmounted — so a remote terminal parked
// on a fatal REMOTE_ATTACH_FAILED banner (reconnect disabled) had no way back
// short of selecting a different session or reloading the page. Selecting a
// DIFFERENT remote or session already changes that key, so the counter only has
// to move for the same one.
//
// Round 8 (#2): it may only move when THIS attempt is in the failed state.
// Round 7 bumped on every re-click, so re-selecting a healthy attached remote —
// glance at the Fleet tab, click the same tile to come back — changed the key,
// defeated the effect's double-init guard, disposed the xterm instance, closed
// the WebSocket and made the server tear the ssh child down: scrollback gone,
// remote tmux client detached and reattached. The local equivalent is
// idempotent, so that was remote-only. The failed state is carried explicitly
// in remoteAttachFailedSignal rather than inferred from the WS state (which is
// briefly 'connecting' during an ordinary healthy reconnect) or by comparing
// selection objects.
export function selectRemoteSession(remote, session) {
  const cur = selectedRemoteSignal.value
  const sameTile = !!cur && cur.remote === remote && !!cur.session && cur.session.id === session.id
  const curAttempt = sameTile ? (cur.attempt || 0) : 0
  const failed =
    sameTile && remoteAttachFailedSignal.value === remoteTerminalKey(remote, session.id, curAttempt)
  // A healthy re-click still refreshes the stored session snapshot (RightRail
  // renders title/status from it) but keeps `attempt` — and therefore
  // remoteTerminalKey, and therefore TerminalPanel's effect deps — unchanged,
  // so the live terminal and its WebSocket are left completely alone.
  const attempt = failed ? curAttempt + 1 : curAttempt
  if (failed) remoteAttachFailedSignal.value = null
  selectedIdSignal.value = null
  selectedRemoteSignal.value = { remote, session, attempt }
}

// SSE connection state: 'connecting' | 'connected' | 'disconnected'
export const connectionSignal = signal('connecting')

// Theme preference: 'light' | 'dark' | 'system'
export const themeSignal = signal(
  localStorage.getItem('theme') || 'system'
)

// Settings from GET /api/settings
export const settingsSignal = signal(null)

// Auth token for API calls (set by app.js after reading from URL)
// Defined in auth.js (leaf module) so api.js can read it without importing
// state.js; re-exported here so existing imports keep working.
export { authTokenSignal } from './auth.js'

// Per-session costs from GET /api/costs/batch (map of sessionId -> costUSD)
export const sessionCostsSignal = signal({})

// Sidebar open state (for tablet/phone responsive toggle)
// LAYT-05: explicit localStorage value wins; otherwise default based on viewport
// (open on tablet/desktop >= 768px, closed on phone < 768px). Prevents the
// mobile sidebar overlay from covering the terminal on cold load.
function initialSidebarOpen() {
  try {
    const stored = localStorage.getItem('agentdeck.sidebarOpen')
    if (stored === 'true') return true
    if (stored === 'false') return false
  } catch (_) {
    // localStorage may throw in incognito/privacy modes; fall through to viewport default.
  }
  return typeof window !== 'undefined' && window.innerWidth >= 768
}
export const sidebarOpenSignal = signal(initialSidebarOpen())

// Sidebar width in pixels, persisted to localStorage. LAYT-01 (BUG #4 + #10).
// Clamped to [200, 480]; default 280. Mobile overlay ignores this (keeps w-72 = 288px).
const SIDEBAR_WIDTH_MIN = 200
const SIDEBAR_WIDTH_MAX = 480
const SIDEBAR_WIDTH_DEFAULT = 280
function clampSidebarWidth(n) {
  if (!Number.isFinite(n)) return SIDEBAR_WIDTH_DEFAULT
  if (n < SIDEBAR_WIDTH_MIN) return SIDEBAR_WIDTH_MIN
  if (n > SIDEBAR_WIDTH_MAX) return SIDEBAR_WIDTH_MAX
  return Math.round(n)
}
function initialSidebarWidth() {
  try {
    const stored = localStorage.getItem('sidebar-width')
    if (stored != null) {
      const n = parseInt(stored, 10)
      return clampSidebarWidth(n)
    }
  } catch (_) {
    // localStorage may throw in incognito/privacy modes; fall through.
  }
  return SIDEBAR_WIDTH_DEFAULT
}
export const sidebarWidthSignal = signal(initialSidebarWidth())
export { SIDEBAR_WIDTH_MIN, SIDEBAR_WIDTH_MAX, SIDEBAR_WIDTH_DEFAULT, clampSidebarWidth }

// Focused session ID for keyboard navigation (NOT array index, stable across SSE updates)
// Lives in state.js (not SessionList.js) so useKeyboardNav.js can import it without a circular dependency.
export const focusedIdSignal = signal(null)

// Dialog open/close signals (Phase 4: mutations)
// createSessionDialogSignal: boolean (true = dialog open)
export const createSessionDialogSignal = signal(false)

// confirmDialogSignal: null or { message: string, onConfirm: function }
export const confirmDialogSignal = signal(null)

// groupNameDialogSignal: null or { mode: 'create'|'rename', groupPath: string, currentName: string, onSubmit: function }
export const groupNameDialogSignal = signal(null)

// editSessionDialogSignal: null or { sessionId: string }
// Mirrors the TUI EditSessionDialog (internal/ui/edit_session_dialog.go) —
// opens a modal that PATCHes /api/sessions/{id}. Closes "Edit session
// settings" MISSING row in tests/web/PARITY_MATRIX.md.
export const editSessionDialogSignal = signal(null)

// WebSocket connection state for terminal: 'disconnected' | 'connecting' | 'connected' | 'error'
export const wsStateSignal = signal('disconnected')

// Read-only mode from WebSocket status:connected payload
export const readOnlySignal = signal(false)

// Push notification state (migrated from app.js state object)
export const pushConfigSignal = signal(null)        // null or { enabled, vapidPublicKey }
export const pushSubscribedSignal = signal(false)
export const pushBusySignal = signal(false)
export const pushEndpointSignal = signal('')

// Info drawer open/close state (Phase 10: replaces showSettings local state in Topbar)
export const infoDrawerOpenSignal = signal(false)

// Sidebar search query (Issue A: search/filter)
export const searchQuerySignal = signal('')
export const searchVisibleSignal = signal(false)

// Global error toasts (Issue F) and toast history (WEB-P0-4 + POL-7) live
// in toasts.js (leaf module) so api.js can raise toasts without importing
// state.js; re-exported here so existing imports keep working.
export { toastsSignal, toastHistorySignal } from './toasts.js'

// Keyboard shortcuts overlay open/close (BUG #14 / UX-03)
export const shortcutsOverlaySignal = signal(false)

// Toast history drawer open/close (WEB-P0-4 + POL-7)
export const toastHistoryOpenSignal = signal(false)

// Mutations gate (WEB-P0-4 prevention layer): when /api/settings returns
// webMutations=false, the UI hides write buttons so users cannot generate
// 403 error spam. Defaults to true (optimistic) until AppShell mount fetches
// /api/settings and assigns the real value.
export const mutationsEnabledSignal = signal(true)

// show_only_installed_tools filter (issue #1259), hydrated from /api/settings
// alongside webMutations. toolFilterSignal: the flag is on. visibleToolsSignal:
// the set of tool names that resolved on PATH; the new-session dialog intersects
// its static tool list against this when the filter is on. toolFilterFallback:
// nothing but shell resolved, so the dialog shows all tools plus a hint. Defaults
// keep the dialog showing every tool until the real values arrive.
export const toolFilterSignal = signal(false)
export const visibleToolsSignal = signal([])
export const toolFilterFallbackSignal = signal(false)
export const hiddenToolsSignal = signal([])
export const pickerToolsSignal = signal([])

// Web terminal link-open policy (issue #1682), hydrated from /api/settings.
// trustedDomainsSignal holds the `[web].trusted_domains` hosts whose links
// open without the confirm; confirmLinkOpenSignal is `[web].confirm_link_open`
// and gates the prompt for every other host. Defaults are the safe ones (no
// trusted hosts, confirm on) so the prompt only relaxes once the real config
// arrives.
export const trustedDomainsSignal = signal([])
export const confirmLinkOpenSignal = signal(true)

// POL-1 (Phase 9, plan 01): sidebar load state for skeleton render gate.
// Initialized false; flipped to true on the first /api/menu response OR the
// first SSE `menu` snapshot in main.js. Never flips back — once the sidebar
// has seen real data, it is past the skeleton phase forever. Per 06-05 STATE.md
// handoff: new signals are APPENDED AT THE TAIL to preserve clean merges.
export const sessionsLoadedSignal = signal(false)

// PR-B: profiles list from GET /api/profiles, hydrated once on AppShell mount.
// Shape: { current: string, profiles: string[] }. The Topbar reads this to
// build the profile dropdown (replacing hardcoded options).
export const profilesSignal = signal(null)

// PR-B: system sysinfo block from GET /api/system/stats, polled every 5s.
// Shape: { cpu, memory, disk, load, gpu?, network }. Each block is only
// present when the underlying collector is Available=true. The Footer reads
// this for live CPU / memory / network indicators. Defaults to null until
// the first poll lands; consumers handle the null case.
export const systemStatsSignal = signal(null)

// Command Center snapshot from the /events/command-center SSE feed (the
// embedded live fleet god-view — see COMMAND-CENTER-DESIGN.md). Shape:
// { profile, generatedAt, conductors[], totals, decisionsWaiting[],
//   recentlyCompleted[], askTargets[] }. Defaults to null until the first
// SSE snapshot lands; the pane handles the null case with a skeleton.
export const commandCenterSignal = signal(null)

export async function loadArchivedSessions() {
  try {
    const data = await apiFetch('GET', '/api/sessions/archived')
    archivedSessionsSignal.value = data.sessions || []
  } catch (_) {
    archivedSessionsSignal.value = []
  }
}
