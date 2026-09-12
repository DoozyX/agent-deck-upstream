import { test, expect } from '@playwright/test'

const mixedCoverage = {
  event_count: 3, total_tokens: 28, known_price_event_count: 1, known_price_tokens: 12,
  known_zero_event_count: 0, unknown_price_event_count: 1, unreconciled_event_count: 1,
  unknown_price_tokens: 7, unreconciled_tokens: 9, coverage_known: true, complete: false,
}

test('costs tab button toggles to cost dashboard', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const costsBtn = page.locator('button.top-tab', { hasText: /^Costs$/ })
  await expect(costsBtn).toBeVisible()

  await costsBtn.click()

  await expect(costsBtn).toHaveClass(/active/)

  // Main area should be visible (cost dashboard renders or shows error/loading)
  const mainArea = page.locator('.main')
  await expect(mainArea).toBeVisible()
})

test('costs tab switches back to terminal', async ({ page }) => {
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  const costsBtn = page.locator('button.top-tab', { hasText: /^Costs$/ })
  const terminalBtn = page.locator('button.top-tab', { hasText: /^Terminal$/ })
  await costsBtn.click()
  await expect(costsBtn).toHaveClass(/active/)

  await terminalBtn.click()
  await expect(terminalBtn).toHaveClass(/active/)
  await expect(costsBtn).not.toHaveClass(/active/)
})

test('costs tab labels mixed coverage and incomplete projection', async ({ page }) => {
	await page.addInitScript(() => {
		localStorage.setItem('agentdeck.tab', JSON.stringify('fleet'))
		if ('serviceWorker' in navigator) navigator.serviceWorker.register = async () => { throw new Error('disabled in route fixture') }
	})
	let summaryRequests = 0
	await page.route(/\/api\/costs\/summary(?:\?|$)/, route => {
		summaryRequests++
		return route.fulfill({ json: {
    today_usd: 1.25, week_usd: 1.25, month_usd: 1.25, projected_usd: 5.35,
    today_events: 3, week_events: 3, month_events: 3,
    today_coverage: mixedCoverage, week_coverage: mixedCoverage, month_coverage: mixedCoverage,
    projection_coverage: mixedCoverage, projection_complete: false,
		} })
	})
  await page.route(/\/api\/costs\/daily(?:\?|$)/, route => route.fulfill({ json: [] }))
  await page.route(/\/api\/costs\/models(?:\?|$)/, route => route.fulfill({ json: { costs: { known: 1.25 }, breakdowns: [] } }))

  await page.goto('/', { waitUntil: 'domcontentloaded' })
	await page.locator('button.top-tab', { hasText: /^Costs$/ }).click()
	await expect.poll(() => summaryRequests).toBe(1)
  await expect(page.getByText('$1.25 known subtotal').first()).toBeVisible()
  await expect(page.getByText(/1 unpriced event \/ 7 tokens/).first()).toBeVisible()
  await expect(page.getByText(/incomplete projection/)).toBeVisible()

  const screenshotDir = process.env.ACCOUNTING_SCREENSHOT_DIR
  if (screenshotDir) {
    await page.screenshot({ path: `${screenshotDir}/after-covered-cost-dashboard.png`, fullPage: true })
  }
})

test('legacy cost API is visibly coverage-unknown', async ({ page }) => {
	await page.addInitScript(() => {
		localStorage.setItem('agentdeck.tab', JSON.stringify('fleet'))
		if ('serviceWorker' in navigator) navigator.serviceWorker.register = async () => { throw new Error('disabled in route fixture') }
	})
	let summaryRequests = 0
	await page.route(/\/api\/costs\/summary(?:\?|$)/, route => {
		summaryRequests++
		return route.fulfill({ json: {
    today_usd: 0, week_usd: 0, month_usd: 0, projected_usd: 0,
    today_events: 1, week_events: 1, month_events: 1,
		} })
	})
  await page.goto('/', { waitUntil: 'domcontentloaded' })
	await page.locator('button.top-tab', { hasText: /^Costs$/ }).click()
	await expect.poll(() => summaryRequests).toBe(1)
  await expect(page.getByText('coverage unknown').first()).toBeVisible()

  const screenshotDir = process.env.ACCOUNTING_SCREENSHOT_DIR
  if (screenshotDir) {
    await page.screenshot({ path: `${screenshotDir}/before-legacy-cost-dashboard.png`, fullPage: true })
  }
})
