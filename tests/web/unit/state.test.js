// unit/state.test.js -- pin behavior of helper utilities exported from
// state.js (the bridge layer between SSE updates and Preact components).
//
// Behavior we care about for parity:
//   - clampSidebarWidth never lets the layout reach a state the TUI
//     equivalent (preview vs. menu split) cannot replicate
//   - localStorage absence/exception falls back to viewport defaults
//     instead of throwing (privacy/incognito mode users)

import { describe, it, expect, beforeEach } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'

describe('clampSidebarWidth', () => {
  beforeEach(() => {
    // Reset modules so per-test localStorage stubs are honored on initial-load.
    // (state.js reads localStorage at import-time for sidebarWidthSignal.)
  })

  it('returns the default for non-finite input', async () => {
    const { clampSidebarWidth, SIDEBAR_WIDTH_DEFAULT } = await import(stateModulePath)
    expect(clampSidebarWidth(NaN)).toBe(SIDEBAR_WIDTH_DEFAULT)
    expect(clampSidebarWidth(undefined)).toBe(SIDEBAR_WIDTH_DEFAULT)
    expect(clampSidebarWidth(Infinity)).toBe(SIDEBAR_WIDTH_DEFAULT)
  })

  it('clamps below MIN', async () => {
    const { clampSidebarWidth, SIDEBAR_WIDTH_MIN } = await import(stateModulePath)
    expect(clampSidebarWidth(50)).toBe(SIDEBAR_WIDTH_MIN)
    expect(clampSidebarWidth(SIDEBAR_WIDTH_MIN - 1)).toBe(SIDEBAR_WIDTH_MIN)
  })

  it('clamps above MAX', async () => {
    const { clampSidebarWidth, SIDEBAR_WIDTH_MAX } = await import(stateModulePath)
    expect(clampSidebarWidth(9999)).toBe(SIDEBAR_WIDTH_MAX)
    expect(clampSidebarWidth(SIDEBAR_WIDTH_MAX + 1)).toBe(SIDEBAR_WIDTH_MAX)
  })

  it('rounds non-integer values inside the valid range', async () => {
    const { clampSidebarWidth } = await import(stateModulePath)
    expect(clampSidebarWidth(280.6)).toBe(281)
    expect(clampSidebarWidth(280.4)).toBe(280)
  })
})

describe('selectLocalSession / selectRemoteSession', () => {
  // state.js is a module singleton, so its signals survive between tests here.
  // Reset the selection surface explicitly rather than letting one case's
  // leftovers decide the next case's starting attempt.
  beforeEach(async () => {
    const { selectedIdSignal, selectedRemoteSignal, remoteAttachFailedSignal } = await import(stateModulePath)
    selectedIdSignal.value = null
    selectedRemoteSignal.value = null
    remoteAttachFailedSignal.value = null
  })

  it('selectLocalSession clears any selected remote session', async () => {
    const { selectLocalSession, selectRemoteSession, selectedIdSignal, selectedRemoteSignal } = await import(stateModulePath)
    selectRemoteSession('m5', { id: 'sess-1', title: 'work' })
    expect(selectedRemoteSignal.value).toEqual({ remote: 'm5', session: { id: 'sess-1', title: 'work' }, attempt: 0 })
    expect(selectedIdSignal.value).toBe(null)

    selectLocalSession('local-1')
    expect(selectedIdSignal.value).toBe('local-1')
    expect(selectedRemoteSignal.value).toBe(null)
  })

  it('selectRemoteSession clears any selected local session', async () => {
    const { selectLocalSession, selectRemoteSession, selectedIdSignal, selectedRemoteSignal } = await import(stateModulePath)
    selectLocalSession('local-1')
    expect(selectedIdSignal.value).toBe('local-1')

    selectRemoteSession('m5', { id: 'sess-1', title: 'work' })
    expect(selectedRemoteSignal.value).toEqual({ remote: 'm5', session: { id: 'sess-1', title: 'work' }, attempt: 0 })
    expect(selectedIdSignal.value).toBe(null)
  })

  // Round 7 (#1) / round 8 (#2): TerminalPanel keys its terminal + WebSocket
  // effect on `remote:<name>:<id>#<attempt>`. Re-clicking the tile that is
  // already selected is the only retry gesture a remote terminal has (there is
  // no Restart action for remotes) — but ONLY when that attempt actually
  // failed. Round 7 bumped on every re-click, which destroyed and rebuilt a
  // perfectly healthy attached remote (scrollback gone, ssh child killed, remote
  // tmux client detached) on the ordinary "switch to Fleet, click the tile to
  // come back" gesture.
  //
  // (a) healthy remote attach, user re-clicks the same tile.
  it('selectRemoteSession leaves attempt alone when re-selecting a HEALTHY remote tile', async () => {
    const { selectRemoteSession, selectedRemoteSignal, remoteAttachFailedSignal, remoteTerminalKey } =
      await import(stateModulePath)

    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    expect(selectedRemoteSignal.value.attempt).toBe(0)
    const key = remoteTerminalKey('build', 'remote-1', 0)
    // Nothing marked this attachment as failed, i.e. it is attached and alive.
    expect(remoteAttachFailedSignal.value).toBe(null)

    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    selectRemoteSession('build', { id: 'remote-1', title: 'release' })

    expect(selectedRemoteSignal.value.attempt).toBe(0)
    // The terminal key is what TerminalPanel's effect is keyed on: unchanged
    // means no teardown, no xterm dispose, no new WebSocket, no new ssh child.
    expect(remoteTerminalKey('build', 'remote-1', selectedRemoteSignal.value.attempt)).toBe(key)
    // A re-click still refreshes the stored session snapshot for the right rail.
    selectRemoteSession('build', { id: 'remote-1', title: 'release (renamed)' })
    expect(selectedRemoteSignal.value.session.title).toBe('release (renamed)')
    expect(selectedRemoteSignal.value.attempt).toBe(0)
  })

  // (b) attach failed with REMOTE_ATTACH_FAILED, user re-clicks the tile.
  it('selectRemoteSession bumps attempt when re-selecting a FAILED remote tile', async () => {
    const { selectRemoteSession, selectedRemoteSignal, remoteAttachFailedSignal, remoteTerminalKey } =
      await import(stateModulePath)

    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    expect(selectedRemoteSignal.value.attempt).toBe(0)

    // What TerminalPanel does on a fatal REMOTE_ATTACH_FAILED frame.
    remoteAttachFailedSignal.value = remoteTerminalKey('build', 'remote-1', 0)

    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    expect(selectedRemoteSignal.value.attempt).toBe(1)
    // Consuming the retry clears the marker, so the NEXT re-click is inert
    // again until the new attempt reports its own failure.
    expect(remoteAttachFailedSignal.value).toBe(null)
    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    expect(selectedRemoteSignal.value.attempt).toBe(1)

    // A marker for a DIFFERENT attempt of the same tile is stale and must not
    // arm a retry.
    remoteAttachFailedSignal.value = remoteTerminalKey('build', 'remote-1', 0)
    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    expect(selectedRemoteSignal.value.attempt).toBe(1)
  })

  // Row (f) of the round-8 scenario matrix: the LOCAL selection path is
  // untouched by the remote retry machinery, and re-selecting the same local
  // session stays the plain idempotent write it has always been.
  it('selectLocalSession is idempotent and never arms the remote retry state', async () => {
    const { selectLocalSession, selectedIdSignal, selectedRemoteSignal, remoteAttachFailedSignal } =
      await import(stateModulePath)

    selectLocalSession('local-1')
    selectLocalSession('local-1')
    expect(selectedIdSignal.value).toBe('local-1')
    expect(selectedRemoteSignal.value).toBe(null)
    expect(remoteAttachFailedSignal.value).toBe(null)
  })

  it('selectRemoteSession restarts the attempt count for a different tile', async () => {
    const { selectRemoteSession, selectedRemoteSignal, remoteAttachFailedSignal, remoteTerminalKey } =
      await import(stateModulePath)

    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    remoteAttachFailedSignal.value = remoteTerminalKey('build', 'remote-1', 0)
    selectRemoteSession('build', { id: 'remote-1', title: 'release' })
    expect(selectedRemoteSignal.value.attempt).toBe(1)

    // A different session on the same remote restarts the count...
    selectRemoteSession('build', { id: 'remote-2', title: 'other' })
    expect(selectedRemoteSignal.value.attempt).toBe(0)
    // ...as does the same session id on a different remote (ids can collide).
    selectRemoteSession('offline', { id: 'remote-2', title: 'other' })
    expect(selectedRemoteSignal.value.attempt).toBe(0)
  })
})

describe('module signals export', () => {
  it('exposes the signals that SSE updates and components share', async () => {
    const state = await import(stateModulePath)
    // These names are the parity surface between vanilla SSE handlers and
    // Preact components. Renaming any of these is a breaking change.
    const required = [
      'sessionsSignal',
      'selectedIdSignal',
      'selectedRemoteSignal',
      'connectionSignal',
      'themeSignal',
      'settingsSignal',
      'authTokenSignal',
      'sessionCostsSignal',
      'sidebarOpenSignal',
      'sidebarWidthSignal',
      'focusedIdSignal',
      'createSessionDialogSignal',
      'confirmDialogSignal',
      'hiddenToolsSignal',
      'pickerToolsSignal',
    ]
    for (const name of required) {
      expect(state[name], `expected exported signal ${name}`).toBeDefined()
      expect(typeof state[name].value).not.toBe('function')
    }
  })
})
