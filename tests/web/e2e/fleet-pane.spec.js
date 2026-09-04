// e2e/fleet-pane.spec.js -- Fleet pane (default tab) end-to-end coverage.
//
// The Fleet pane (internal/web/static/app/panes/FleetPane.js) is the cold-load
// landing surface: activeTabSignal defaults to 'fleet' (uiState.js). It renders
// stat tiles computed from menuModelSignal plus one GroupCard per non-empty
// group. All assertions are grounded in the fixture seed
// (tests/web/fixtures/cmd/web-fixture/main.go seed()):
//
//   sess-001 "agent-deck"     tool=claude status=idle    group=work           path=/srv/agent-deck
//   sess-002 "frontend"       tool=claude status=running group=work           path=/srv/frontend
//   sess-003 "innotrade-api"  tool=codex  status=idle    group=work/innotrade path=/srv/innotrade-api
//   sess-004 "scratch"        tool=shell  status=idle    group=personal       path=/home/dev/scratch
//
// → counts: running=1, waiting=0, error=0, idle=3, sessions=4
// → groups (labels are uppercased by dataModel.js projectGroup):
//     WORK (2 sessions), INNOTRADE (1 session), PERSONAL (1 session)
//
// The Fleet pane renders on ALL viewports (phone gets dedicated CSS tweaks in
// app.css @media (max-width: 720px) and a Fleet entry in MobileTabs), so no
// phone skips here — every test runs on chromium-desktop/tablet/phone.

import { test, expect } from '@playwright/test'

test.describe('fleet pane', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('cold load lands on the Fleet tab', async ({ page }) => {
    // Fresh browser context → no persisted agentdeck.tab in localStorage →
    // activeTabSignal falls back to its 'fleet' default.
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })
    // The always-mounted terminal pane stays CSS-hidden while Fleet is active.
    await expect(page.locator('.term-wrap')).toBeHidden()
  })

  test('stat tiles show counts derived from the fixture seed', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })
    // Seed: sess-002 running; sess-001/003/004 idle; nothing waiting/error.
    // toHaveText retries, which absorbs the initial empty render before the
    // first SSE menu snapshot hydrates sessionsSignal.
    await expect(page.locator('[data-testid="fleet-stat-running"] .num')).toHaveText('1')
    await expect(page.locator('[data-testid="fleet-stat-waiting"] .num')).toHaveText('0')
    await expect(page.locator('[data-testid="fleet-stat-error"] .num')).toHaveText('0')
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('3')
    await expect(page.locator('[data-testid="fleet-stat-sessions"] .num')).toHaveText('4')
  })

  test('group cards render seeded groups with session count footers', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    const cards = page.locator('[data-testid="fleet-group-card"]')
    await expect(cards).toHaveCount(3)

    // dataModel.js projectGroup uppercases names into labels; GroupCard
    // receives that label as `name` and we mirror it into data-group-name.
    const work = page.locator('[data-testid="fleet-group-card"][data-group-name="WORK"]')
    const innotrade = page.locator('[data-testid="fleet-group-card"][data-group-name="INNOTRADE"]')
    const personal = page.locator('[data-testid="fleet-group-card"][data-group-name="PERSONAL"]')

    await expect(work).toBeVisible()
    await expect(work.locator('[data-testid="fleet-group-session-count"]')).toHaveText('2 sessions')
    // work holds the seed's agent-deck + frontend tiles.
    await expect(work.locator('[data-testid="fleet-session-tile"]')).toHaveCount(2)

    await expect(innotrade).toBeVisible()
    await expect(innotrade.locator('[data-testid="fleet-group-session-count"]')).toHaveText('1 session')

    await expect(personal).toBeVisible()
    await expect(personal.locator('[data-testid="fleet-group-session-count"]')).toHaveText('1 session')
  })

  test('clicking a session tile selects it and switches to the Terminal tab', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    const tile = page.locator('[data-testid="fleet-session-tile"][data-session-id="sess-003"]')
    await expect(tile).toBeVisible()
    await expect(tile).toContainText('innotrade-api')
    await tile.click()

    // FleetPane.onSelect sets selectedIdSignal=sess-003 + activeTabSignal='terminal':
    // the fleet pane unmounts, the terminal wrapper becomes visible, and the
    // work-head breadcrumb shows the selected session's title. These checks
    // hold on phone too (work-head + term-wrap survive the ≤720px layout).
    await expect(page.locator('[data-testid="fleet-pane"]')).toHaveCount(0)
    await expect(page.locator('.term-wrap')).toBeVisible()
    await expect(page.locator('.work-head .cur')).toHaveText('innotrade-api')
  })

  test('live update: status change is reflected in stat tiles within ~2s', async ({ page, request }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    // Pin the starting state so the post-mutation assertion can't false-pass.
    await expect(page.locator('[data-testid="fleet-stat-waiting"] .num')).toHaveText('0')
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('3')

    // Simulate a TUI-side transition through the fixture admin endpoint.
    // This bypasses the web mutator (no immediate SSE broadcast), so the
    // change rides the menu stream's 2s poll tick (handlers_events.go
    // menuEventsPollInterval) — 4s is a comfortable bound, same spirit as
    // the children-panel live-update test.
    const res = await request.post('/__fixture/session/sess-001/status?to=waiting')
    expect(res.status()).toBe(204)

    await expect(page.locator('[data-testid="fleet-stat-waiting"] .num')).toHaveText('1', { timeout: 4000 })
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('2')
    // Untouched tiles stay put.
    await expect(page.locator('[data-testid="fleet-stat-running"] .num')).toHaveText('1')
    await expect(page.locator('[data-testid="fleet-stat-sessions"] .num')).toHaveText('4')
  })

  test('configured remotes render through the production API alongside local groups', async ({ page, request }) => {
    await request.post('/__fixture/remotes')
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-remote-card"]')).toHaveCount(2)
    await expect(page.locator('[data-testid="fleet-remote-card"][data-remote-name="build"]'))
      .toContainText('24ms')
    await expect(page.locator('[data-testid="fleet-remote-session-tile"]')).toHaveCount(3)
    await expect(page.locator('[data-testid="fleet-remote-card"][data-remote-name="offline"]')).toContainText('last-known')
    await expect(page.locator('[data-testid="fleet-remote-age"]')).toHaveText('Last known state · 37s ago')
    await expect(page.locator('[data-testid="fleet-stat-remotes"]')).toHaveText('1/2 remotes online')
    await expect(page.locator('[data-testid="fleet-stat-sessions"] .num')).toHaveText('7')
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(3)
  })

  test('clicking a remote session tile attaches a terminal over the SSH bridge', async ({ page, request }) => {
    await request.post('/__fixture/remotes')
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    const tile = page.locator('[data-testid="fleet-remote-card"][data-remote-name="build"] ' +
      '[data-testid="fleet-remote-session-tile"][data-session-id="remote-1"]')
    await expect(tile).toBeVisible()

    // selectRemoteSession(remote, session) + activeTabSignal='terminal': the
    // fleet pane unmounts and TerminalPanel opens a
    // /ws/remote/build/session/remote-1 connection to the fixture's
    // RemoteAttachCommand (`sh -c 'printf "remote-shell\r\n"; cat'`), not the
    // local /ws/session/ path. xterm.js's accessibility tree is off by
    // default (no screenReaderMode), so the rendered bytes aren't queryable
    // DOM text; assert on the WS frame the server actually streamed instead
    // — that's the thing this design item adds.
    const wsPromise = page.waitForEvent('websocket', ws => ws.url().includes('/ws/remote/build/session/remote-1'))
    await tile.click()
    const ws = await wsPromise

    // Register the frame listener BEFORE any other awaits: the connect /
    // terminal_attached / first data frames can all land before the next
    // line of this test runs, and a listener attached later would miss them.
    const sawRemoteShellPromise = new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('timed out waiting for "remote-shell" over /ws/remote/')), 5000)
      ws.on('framereceived', (frame) => {
        const text = Buffer.isBuffer(frame.payload) ? frame.payload.toString('utf8') : String(frame.payload)
        if (text.includes('remote-shell')) {
          clearTimeout(timer)
          resolve(true)
        }
      })
    })

    await expect(page.locator('[data-testid="fleet-pane"]')).toHaveCount(0)
    await expect(page.locator('.term-wrap')).toBeVisible()
    expect(await sawRemoteShellPromise).toBe(true)

    // Remote attach is view-only: the work head shows "REMOTE <remote> /
    // <title>" and hides Start/Restart/New/Fork (design: attach-only scope).
    await expect(page.locator('.work-head .path')).toContainText('REMOTE')
    await expect(page.locator('.work-head .path')).toContainText('build')
    await expect(page.locator('.work-head .cur')).toHaveText('release')
    await expect(page.locator('.work-head .actions')).toHaveCount(0)
  })

  // Regression guard for AppShell.js focusedSession()'s
  // `if (selectedRemoteSignal.value) return null` line. selectRemoteSession()
  // clears selectedIdSignal (state.js, mutually exclusive), so without that
  // guard every local-session shortcut falls through to `sessions[0]` and acts
  // on an unrelated LOCAL session while the user is looking at a remote one —
  // 'D' would pop a close-session confirm for fixture sess-001 "agent-deck".
  // The unit suite only covers the signal mutual-exclusivity; this covers the
  // handler.
  test('local-session shortcuts no-op while a remote session is selected', async ({ page, context, request }) => {
    await request.post('/__fixture/remotes')
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    const tile = page.locator('[data-testid="fleet-remote-card"][data-remote-name="build"] ' +
      '[data-testid="fleet-remote-session-tile"][data-session-id="remote-1"]')
    await tile.click()
    await expect(page.locator('.work-head .path')).toContainText('REMOTE')
    await expect(page.locator('.work-head .cur')).toHaveText('release')

    // The right rail must describe the REMOTE session, not fall back to
    // sessions[0] (fixture sess-001 "agent-deck"/claude/work) the way it did
    // while selectedIdSignal was null.
    const rail = page.locator('[data-testid="right-rail"]')
    await expect(rail).toHaveAttribute('data-remote-name', 'build')
    await expect(rail).toContainText('release')
    await expect(rail.locator('[data-testid="rail-card-overview"]')).toContainText('codex')
    await expect(rail.locator('[data-testid="rail-card-overview"]')).toContainText('/srv/release')
    await expect(rail).not.toContainText('agent-deck')

    // Attaching hands keyboard focus to xterm.js, whose helper <textarea>
    // trips AppShell's `inField` guard and would swallow every key below,
    // making this test vacuous (Escape does not help — xterm re-focuses it).
    // Clicking the inert work-head text moves focus off the terminal the way
    // a user reaching for a global shortcut would, and the '?' toggle then
    // proves keys really do reach the window-level shortcut handler before
    // the no-op assertions run.
    await page.locator('.work-head .path').click()
    await expect(page.locator('.xterm-helper-textarea')).not.toBeFocused()
    await page.keyboard.press('?')
    await expect(page.locator('[data-testid="shortcuts-overlay"]')).toBeVisible()
    await page.keyboard.press('?')
    await expect(page.locator('[data-testid="shortcuts-overlay"]')).toHaveCount(0)

    // Shift+D: must not open the close-session confirm for a local session.
    await page.keyboard.down('Shift')
    await page.keyboard.press('D')
    await page.keyboard.up('Shift')
    // Round-trip the overlay again so the would-be dialog gets real time to
    // render before the negative assertion — cheaper and less flaky than a
    // fixed sleep.
    await page.keyboard.press('?')
    await expect(page.locator('[data-testid="shortcuts-overlay"]')).toBeVisible()
    await page.keyboard.press('?')
    await expect(page.locator('[data-testid="shortcuts-overlay"]')).toHaveCount(0)
    await expect(page.locator('.dialog', { hasText: /close session/i })).toHaveCount(0)

    // Shift+Enter: reads the same guard to window.open a session in a new
    // browser tab (AppShell.js, checked BEFORE bare Enter). Must open nothing.
    const strayTabPromise = context.waitForEvent('page', { timeout: 2000 }).catch(() => null)
    await page.keyboard.down('Shift')
    await page.keyboard.press('Enter')
    await page.keyboard.up('Shift')
    expect(await strayTabPromise).toBeNull()

    // Enter: must not swap the terminal over to sessions[0].
    await page.keyboard.press('Enter')
    await expect(page.locator('.work-head .path')).toContainText('REMOTE')
    await expect(page.locator('.work-head .cur')).toHaveText('release')

    // j/k list navigation does not read focusedSession() — it calls
    // selectLocalSession() directly — so it needs its own guard, and its own
    // assertion: without one, j silently swaps the remote terminal for a
    // local session.
    await page.keyboard.press('j')
    await expect(page.locator('.work-head .path')).toContainText('REMOTE')
    await page.keyboard.press('k')
    await expect(page.locator('.work-head .path')).toContainText('REMOTE')
    await expect(page.locator('.work-head .cur')).toHaveText('release')

    // 'r' (rename) reads through the same focusedSession(); no toast either.
    await page.keyboard.press('r')
    await expect(page.locator('.toast')).toHaveCount(0)
  })

  // The mirror image of the test above: shortcuts must not act PAST a remote
  // selection, but the affordances that deliberately switch to a local session
  // must actually get there. SearchPane.onSelect wrote selectedIdSignal
  // directly, leaving selectedRemoteSignal set, and TerminalPanel's
  // `remote ? ... : id` priority then kept the remote terminal on screen — the
  // click did nothing visible.
  test('a search result click switches away from a selected remote session', async ({ page, request, viewport }) => {
    // desktop/tablet-only: .top-tabs is hidden at ≤720px and MobileTabs has no
    // Search entry, so there is no in-app Search affordance on phone (same
    // scoping as search-pane.spec.js's Topbar navigation test). Reaching it by
    // localStorage preseed would need a reload, which clears the remote
    // selection this test is about.
    test.skip((viewport?.width || 1280) < 768, 'phone viewport: Topbar tabs hidden; no Search affordance')

    await request.post('/__fixture/remotes')
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    await page.locator('[data-testid="fleet-remote-card"][data-remote-name="build"] ' +
      '[data-testid="fleet-remote-session-tile"][data-session-id="remote-1"]').click()
    await expect(page.locator('.work-head .path')).toContainText('REMOTE')

    await page.locator('.top-tab', { hasText: 'Search' }).click()
    await expect(page.locator('[data-testid="search-pane"]')).toBeVisible()
    await page.locator('[data-testid="search-result"][data-session-id="sess-002"]').click()

    // Local session wins: no REMOTE kicker, and the work head names it.
    await expect(page.locator('.work-head .cur')).toHaveText('frontend')
    await expect(page.locator('.work-head .path')).not.toContainText('REMOTE')
  })

  // App.js's popstate handler wrote selectedIdSignal directly, so Back out of
  // a remote selection restored the URL and the sidebar highlight while
  // selectedRemoteSignal stayed set — and WorkHead/RightRail/TerminalPanel all
  // give the remote priority, so the URL and the rendered session disagreed
  // with both signals set at once.
  test('browser Back out of a remote selection restores the local session', async ({ page, request }) => {
    await request.post('/__fixture/remotes')
    // Deep-link the local selection rather than clicking the sidebar row: a
    // sidebar click also switches the tab to 'terminal', which unmounts the
    // fleet pane and the remote tile this test needs next (same reason
    // url-routing.spec.js deep-links).
    await page.goto('/s/sess-002')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })
    await expect(page.locator('.sess.sel .tt')).toHaveText('frontend')

    // History: /s/sess-002 -> / (the remote tile pushes '/').

    await page.locator('[data-testid="fleet-remote-card"][data-remote-name="build"] ' +
      '[data-testid="fleet-remote-session-tile"][data-session-id="remote-1"]').click()
    await expect(page.locator('.work-head .path')).toContainText('REMOTE')
    await expect.poll(() => new URL(page.url()).pathname).toBe('/')

    await page.goBack()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/s/sess-002')
    // The URL, the rail and the work head must agree: local frontend, no
    // leftover remote selection.
    await expect(page.locator('.work-head .cur')).toHaveText('frontend')
    await expect(page.locator('.work-head .path')).not.toContainText('REMOTE')
    await expect(page.locator('[data-testid="right-rail"]')).not.toHaveAttribute('data-remote-name')
    await expect(page.locator('.sess.sel .tt')).toHaveText('frontend')
  })
})
