// App.js -- Root Preact component (app shell)
// Phase 3: full-page layout with responsive sidebar.
// Phase 6: adds popstate route sync and URL push on selection change.
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { AppShell } from './AppShell.js'
import { selectedIdSignal, selectLocalSession } from './state.js'

export function App() {
  // Route sync: update selectedIdSignal when browser navigates back/forward
  useEffect(() => {
    function onPopState() {
      const path = window.location.pathname || '/'
      if (path.startsWith('/s/')) {
        const raw = path.slice(3)
        if (raw && !raw.includes('/')) {
          // selectLocalSession, not a bare selectedIdSignal write: it also
          // clears selectedRemoteSignal. A bare write leaves both signals set,
          // and TerminalPanel/WorkHead/RightRail all give the remote priority
          // — Back would move the URL and the sidebar highlight while the
          // remote session stayed on screen.
          try {
            selectLocalSession(decodeURIComponent(raw))
          } catch (_) {
            selectLocalSession(null)
          }
          return
        }
      }
      // Clearing on popstate to / lets the empty dashboard render when the user navigates back.
      if (path === '/') {
        selectLocalSession(null)
      }
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  // URL push: write URL when selected session changes
  useEffect(() => {
    const id = selectedIdSignal.value
    const currentPath = window.location.pathname
    const targetPath = id ? '/s/' + encodeURIComponent(id) : '/'
    if (currentPath !== targetPath) {
      window.history.pushState(null, '', targetPath)
    }
  }, [selectedIdSignal.value])

  return html`
    <${AppShell} />
  `
}
