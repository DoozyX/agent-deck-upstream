import { describe, expect, it } from 'vitest'

const dashboardModule = '../../../internal/web/static/app/CostDashboard.js'

describe('cost dashboard coverage labels', () => {
  it('distinguishes unknown, verified zero, and mixed known subtotals', async () => {
    const { costDisplay, coverageLine } = await import(dashboardModule)
    const unknown = { event_count: 1, known_price_event_count: 0, unknown_price_event_count: 1, unknown_price_tokens: 12, unreconciled_event_count: 0, coverage_known: true, complete: false }
    const zero = { event_count: 1, known_price_event_count: 1, known_zero_event_count: 1, unknown_price_event_count: 0, unreconciled_event_count: 0, coverage_known: true, complete: true }
    const mixed = { event_count: 3, known_price_event_count: 1, unknown_price_event_count: 1, unknown_price_tokens: 7, unreconciled_event_count: 1, unreconciled_tokens: 9, coverage_known: true, complete: false }

    expect(costDisplay(0, unknown)).toBe('price unknown')
    expect(costDisplay(0, zero)).toContain('$0.00 (verified)')
    expect(costDisplay(1.25, mixed)).toContain('$1.25 known subtotal')
    expect(coverageLine(mixed)).toBe('1 unpriced event / 7 tokens; 1 unreconciled event / 9 tokens')
  })

  it('marks incomplete projections explicitly', async () => {
    const { costDisplay } = await import(dashboardModule)
    const incomplete = { event_count: 2, known_price_event_count: 1, unknown_price_event_count: 1, coverage_known: true, complete: false }
    expect(costDisplay(3.5, incomplete, true)).toContain('known subtotal · incomplete projection')
  })
})
