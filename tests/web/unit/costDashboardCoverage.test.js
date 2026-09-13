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

  it('does not call incomplete empty-counter coverage complete', async () => {
	const { coverageLine } = await import(dashboardModule)
	expect(coverageLine({ coverage_known: true, complete: false, event_count: 0 })).toBe('coverage incomplete')
  })

  it('reserves verified zero for events explicitly quoted at zero', async () => {
	const { costDisplay } = await import(dashboardModule)
	const roundedPaid = { event_count: 1, known_price_event_count: 1, known_zero_event_count: 0, coverage_known: true, complete: true }
	const mixedKnown = { event_count: 2, known_price_event_count: 2, known_zero_event_count: 1, coverage_known: true, complete: true }
	expect(costDisplay(0, roundedPaid)).toBe('$0.00')
	expect(costDisplay(0, mixedKnown)).toBe('$0.00')
  })

  it('builds daily and model chart labels from covered breakdowns', async () => {
	const { buildCoveredChartData } = await import(dashboardModule)
	const unknown = { event_count: 1, known_price_event_count: 0, unknown_price_event_count: 1, coverage_known: true, complete: false }
	const mixed = { event_count: 2, known_price_event_count: 1, unknown_price_event_count: 1, coverage_known: true, complete: false }
	const zero = { event_count: 1, known_price_event_count: 1, known_zero_event_count: 1, coverage_known: true, complete: true }
	const got = buildCoveredChartData(
	  [{ date: '2026-09-13', cost_usd: 1.25, coverage: mixed }, { date: '2026-09-14', cost_usd: 0, coverage: unknown }],
	  { costs: { legacy: 99 }, breakdowns: [
		{ key: 'future-model', known_cost_microdollars: 0, coverage: unknown },
		{ key: 'free-model', known_cost_microdollars: 0, coverage: zero },
	  ] },
	)
	expect(got.daily.labels).toEqual(['09-13 · known subtotal', '09-14 · price unknown'])
	expect(got.daily.values).toEqual([1.25, 0])
	expect(got.models.labels).toEqual(['future-model · price unknown', 'free-model · verified'])
	expect(got.models.values).toEqual([0, 0])
  })
})
