// CostDashboard.js -- In-app cost dashboard tab (Preact component)
// Replaces the standalone /costs page as an in-app tab with summary cards and Chart.js charts.
import { html } from 'htm/preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { apiFetch } from './api.js'

// Lazy-loader for Chart.js UMD bundle (issue #1022 part 2). Chart.js is
// ~206 KB and only the Costs route consumes it, so it must not ship in
// the initial payload. The first call injects a <script> tag pointing at
// the same /static/chart.umd.min.js asset the eager <script> used to
// load; subsequent calls return the cached promise so concurrent mounts
// don't race or re-fetch. Resolves with window.Chart once the global is
// set, or rejects if the script tag errors.
let chartLoaderPromise = null
function loadChartJs() {
  if (typeof window === 'undefined') return Promise.reject(new Error('no window'))
  if (window.Chart) return Promise.resolve(window.Chart)
  if (chartLoaderPromise) return chartLoaderPromise
  chartLoaderPromise = new Promise((resolve, reject) => {
    const s = document.createElement('script')
    s.src = '/static/chart.umd.min.js'
    s.async = true
    s.onload = () => {
      if (window.Chart) resolve(window.Chart)
      else reject(new Error('chart.umd.min.js loaded but window.Chart missing'))
    }
    s.onerror = () => {
      chartLoaderPromise = null
      reject(new Error('failed to load chart.umd.min.js'))
    }
    document.head.appendChild(s)
  })
  return chartLoaderPromise
}

// POL-5 (Phase 9, plan 02): locale-aware currency formatting. Constructed
// once at module load — Intl.NumberFormat is non-trivial to build (reads
// ICU data) and the user's locale does not change during a session. Both
// the summary card fmt() helper and the Chart.js y-axis tick callback
// delegate to this instance so they never drift. Currency stays USD
// regardless of locale (no conversion) — only symbol placement, digit
// grouping, and decimal separator follow navigator.language.
const currencyFormatter = new Intl.NumberFormat(navigator.language, {
  style: 'currency',
  currency: 'USD',
})

function fmt(v) {
  return currencyFormatter.format(v || 0)
}

export function coverageLine(coverage) {
  if (!coverage) return 'coverage unknown'
  const parts = []
  if (coverage.unknown_price_event_count > 0) {
    const n = coverage.unknown_price_event_count
    parts.push(`${n} unpriced event${n === 1 ? '' : 's'} / ${coverage.unknown_price_tokens || 0} tokens`)
  }
  if (coverage.unreconciled_event_count > 0) {
    const n = coverage.unreconciled_event_count
    parts.push(`${n} unreconciled event${n === 1 ? '' : 's'} / ${coverage.unreconciled_tokens || 0} tokens`)
  }
  if (parts.length > 0) return parts.join('; ')
  return coverage.coverage_known === false ? 'coverage unknown' : 'coverage complete'
}

export function costDisplay(amount, coverage, projection = false) {
  let value
  if (!coverage || coverage.coverage_known === false) {
    value = 'coverage unknown'
  } else if (coverage.event_count > 0 && coverage.known_price_event_count === 0 &&
      (coverage.unknown_price_event_count > 0 || coverage.unreconciled_event_count > 0)) {
    value = 'price unknown'
  } else if (coverage.complete) {
    value = fmt(amount)
    if (amount === 0 && coverage.event_count > 0) value += ' (verified)'
  } else {
    value = `${fmt(amount)} known subtotal`
  }
  if (projection && (!coverage || !coverage.complete)) value += ' · incomplete projection'
  return value
}

// readChartTheme reads chart palette CSS variables from the document root.
// Variables are defined in internal/web/static/styles.src.css under :root
// (light) and html.dark (dark override). The MutationObserver wired up
// inside CostDashboard's useEffect re-runs buildCharts() whenever the
// theme class on <html> changes, so this helper produces the live palette.
// Per BUG #13 / UX-02 — replaces the legacy CHART_COLORS constant + isDark
// ternary that hardcoded every chart color in JS.
function readChartTheme() {
  const cs = getComputedStyle(document.documentElement)
  const v = (name, fallback) => (cs.getPropertyValue(name).trim() || fallback)
  return {
    text:        v('--chart-text',         '#6b7280'),
    grid:        v('--chart-grid',         '#e5e7eb'),
    legend:      v('--chart-legend',       '#374151'),
    primary:     v('--chart-primary',      '#2959aa'),
    primaryFill: v('--chart-primary-fill', 'rgba(41, 89, 170, 0.1)'),
    categorical: [
      v('--chart-categorical-1', '#7aa2f7'),
      v('--chart-categorical-2', '#bb9af7'),
      v('--chart-categorical-3', '#7dcfff'),
      v('--chart-categorical-4', '#9ece6a'),
      v('--chart-categorical-5', '#e0af68'),
      v('--chart-categorical-6', '#f7768e'),
      v('--chart-categorical-7', '#73daca'),
      v('--chart-categorical-8', '#ff9e64'),
    ],
  }
}

export function CostDashboard() {
  const [summary, setSummary] = useState(null)
  const [error, setError] = useState(null)
  const [loading, setLoading] = useState(true)

  const dailyCanvasRef = useRef(null)
  const modelCanvasRef = useRef(null)
  const dailyChartRef = useRef(null)
  const modelChartRef = useRef(null)

  // Load summary cards
  useEffect(() => {
    apiFetch('GET', '/api/costs/summary')
      .then(data => {
        setSummary(data)
        setLoading(false)
      })
      .catch(err => {
        setError(err.message || 'Failed to load cost data')
        setLoading(false)
      })
  }, [])

  // Build charts after loading (or when canvases become available)
  useEffect(() => {
    if (loading || error) return
    if (!dailyCanvasRef.current || !modelCanvasRef.current) return

    let cancelled = false

    async function buildCharts() {
      try {
        const [Chart, dailyData, modelsData] = await Promise.all([
          loadChartJs(),
          apiFetch('GET', '/api/costs/daily?days=30'),
          apiFetch('GET', '/api/costs/models?coverage=1'),
        ])

        if (cancelled) return

        // Destroy old chart instances before creating new ones
        if (dailyChartRef.current) {
          dailyChartRef.current.destroy()
          dailyChartRef.current = null
        }
        if (modelChartRef.current) {
          modelChartRef.current.destroy()
          modelChartRef.current = null
        }

        if (!dailyCanvasRef.current || !modelCanvasRef.current) return

        // Theme-aware chart colors read from CSS custom properties on
        // document.documentElement. The MutationObserver below re-runs
        // buildCharts() whenever the `dark` class toggles, so this read
        // always reflects the active theme without a page reload.
        const t = readChartTheme()

        const dates = dailyData || []
        const labels = dates.map(d => d.date.slice(5))
        const costs = dates.map(d => d.cost_usd)

        dailyChartRef.current = new Chart(dailyCanvasRef.current, {
          type: 'line',
          data: {
            labels,
            datasets: [{
              label: 'Daily Cost ($)',
              data: costs,
              borderColor: t.primary,
              backgroundColor: t.primaryFill,
              fill: true,
              tension: 0.3,
            }],
          },
          options: {
            responsive: true,
            plugins: { legend: { display: false } },
            scales: {
              x: { ticks: { color: t.text }, grid: { color: t.grid } },
              y: {
                ticks: { color: t.text, callback: v => currencyFormatter.format(v || 0) },
                grid: { color: t.grid },
              },
            },
          },
        })

        const models = modelsData?.costs || modelsData || {}
        const mLabels = Object.keys(models)
        const mData = Object.values(models)

        modelChartRef.current = new Chart(modelCanvasRef.current, {
          type: 'doughnut',
          data: {
            labels: mLabels,
            datasets: [{
              data: mData,
              backgroundColor: t.categorical.slice(0, mLabels.length),
            }],
          },
          options: {
            responsive: true,
            plugins: {
              legend: {
                position: 'bottom',
                labels: { color: t.legend, font: { size: 11 } },
              },
            },
          },
        })
      } catch (_err) {
        // Charts are optional; summary cards still display
      }
    }

    buildCharts()

    // Re-build charts when the theme class on <html> changes (BUG #13 / UX-02).
    // The MutationObserver replaces the previous theme signal dep — the root
    // element class is the source of truth for theme, and reading via
    // getComputedStyle gives Chart.js the new CSS variable values without
    // requiring the parent to re-mount the component.
    const observer = new MutationObserver(() => {
      buildCharts()
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })

    return () => {
      cancelled = true
      observer.disconnect()
    }
  }, [loading, error])

  // Cleanup chart instances on unmount
  useEffect(() => {
    return () => {
      if (dailyChartRef.current) {
        dailyChartRef.current.destroy()
        dailyChartRef.current = null
      }
      if (modelChartRef.current) {
        modelChartRef.current.destroy()
        modelChartRef.current = null
      }
    }
  }, [])

  if (loading) {
    return html`
      <div style="padding: 18px; font-family: var(--mono); font-size: 12px; color: var(--muted);">
        Loading cost data…
      </div>
    `
  }

  if (error) {
    return html`
      <div class="chart-card" style="margin: 14px;">
        <div class="title">Cost tracking unavailable</div>
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); line-height: 1.6;">
          Start agent-deck with the cost tracker enabled to see spend, daily history, and per-model
          breakdowns here. The fixture binary intentionally runs without it.
        </div>
      </div>
    `
  }

  return html`
    <div style="display: flex; flex-direction: column; gap: 12px; flex: 1; min-height: 0; overflow: auto;">
      <div class="stat-grid">
        <div class="stat">
          <div class="lab">TODAY</div>
		  <div class="val">${costDisplay(summary.today_usd, summary.today_coverage)}</div>
		  <div class="delta">${summary.today_events} events · ${coverageLine(summary.today_coverage)}</div>
        </div>
        <div class="stat">
          <div class="lab">THIS WEEK</div>
		  <div class="val">${costDisplay(summary.week_usd, summary.week_coverage)}</div>
		  <div class="delta">${summary.week_events} events · ${coverageLine(summary.week_coverage)}</div>
        </div>
        <div class="stat">
          <div class="lab">THIS MONTH</div>
		  <div class="val">${costDisplay(summary.month_usd, summary.month_coverage)}</div>
		  <div class="delta">${summary.month_events} events · ${coverageLine(summary.month_coverage)}</div>
        </div>
        <div class="stat">
          <div class="lab">PROJECTED</div>
		  <div class="val">${costDisplay(summary.projected_usd, summary.projection_coverage, true)}</div>
		  <div class="delta">based on 7-day known-price subtotal · ${coverageLine(summary.projection_coverage)}</div>
        </div>
      </div>
      <div style="display: grid; grid-template-columns: 2fr 1fr; gap: 12px;">
        <div class="chart-card">
          <div class="title">Daily spend · last 30 days</div>
          <canvas ref=${dailyCanvasRef}></canvas>
        </div>
        <div class="chart-card">
          <div class="title">Cost by model</div>
          <canvas ref=${modelCanvasRef}></canvas>
        </div>
      </div>
    </div>
  `
}
