package costs

import (
	"database/sql"
	"time"
)

type BudgetAction int

const (
	BudgetActionNone BudgetAction = iota
	BudgetActionWarn
	BudgetActionStop
)

type BudgetResult struct {
	Action             BudgetAction
	Reason             string
	UsedMicro          int64
	LimitMicro         int64
	Percentage         float64
	CoverageIncomplete bool
	UnknownPriceTokens int64
	UnreconciledTokens int64
}

// BudgetConfig holds budget limits in microdollars.
type BudgetConfig struct {
	DailyLimit    int64
	WeeklyLimit   int64
	MonthlyLimit  int64
	GroupLimits   map[string]int64 // group name -> daily limit in microdollars
	SessionLimits map[string]int64 // session ID -> total lifetime limit in microdollars
	Timezone      *time.Location   // for determining day/week/month boundaries
}

type BudgetChecker struct {
	cfg   BudgetConfig
	store *Store
}

func NewBudgetChecker(cfg BudgetConfig, store *Store) *BudgetChecker {
	return &BudgetChecker{cfg: cfg, store: store}
}

// CheckTx evaluates all budget limits within a transaction.
// This must be called within the same transaction as the cost event INSERT.
func (b *BudgetChecker) CheckTx(tx *sql.Tx, sessionID, groupName string, groupSessionIDs []string) (BudgetResult, error) {
	worst := BudgetResult{Action: BudgetActionNone}
	tz := b.cfg.Timezone
	if tz == nil {
		tz = time.Local
	}

	// Session lifetime limit
	if limit, ok := b.cfg.SessionLimits[sessionID]; ok && limit > 0 {
		total, err := b.store.RunningTotal(tx, sessionID, time.Time{}) // all time
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, limit, "session lifetime limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		coverage, err := budgetCoverageTx(tx, `session_id = ?`, sessionID)
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget coverage query failed"}, err
		}
		worst = applyBudgetCoverage(worst, coverage, total, limit)
	}

	// Daily global limit
	if b.cfg.DailyLimit > 0 {
		total, err := b.store.GlobalRunningTotal(tx, startOfDay(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, b.cfg.DailyLimit, "daily global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		coverage, err := budgetCoverageTx(tx, `timestamp >= ?`, startOfDay(tz).UTC().Format(time.RFC3339Nano))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget coverage query failed"}, err
		}
		worst = applyBudgetCoverage(worst, coverage, total, b.cfg.DailyLimit)
	}

	// Weekly global limit
	if b.cfg.WeeklyLimit > 0 {
		total, err := b.store.GlobalRunningTotal(tx, startOfWeek(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, b.cfg.WeeklyLimit, "weekly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		coverage, err := budgetCoverageTx(tx, `timestamp >= ?`, startOfWeek(tz).UTC().Format(time.RFC3339Nano))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget coverage query failed"}, err
		}
		worst = applyBudgetCoverage(worst, coverage, total, b.cfg.WeeklyLimit)
	}

	// Monthly global limit
	if b.cfg.MonthlyLimit > 0 {
		total, err := b.store.GlobalRunningTotal(tx, startOfMonth(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, b.cfg.MonthlyLimit, "monthly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		coverage, err := budgetCoverageTx(tx, `timestamp >= ?`, startOfMonth(tz).UTC().Format(time.RFC3339Nano))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget coverage query failed"}, err
		}
		worst = applyBudgetCoverage(worst, coverage, total, b.cfg.MonthlyLimit)
	}

	// Group daily limit
	if limit, ok := b.cfg.GroupLimits[groupName]; ok && limit > 0 && len(groupSessionIDs) > 0 {
		total, err := b.store.GroupRunningTotal(tx, groupSessionIDs, startOfDay(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, limit, "group daily limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		placeholders := "?" + repeatArg(len(groupSessionIDs)-1)
		where := "session_id IN (" + placeholders + ") AND timestamp >= ?"
		args := make([]any, 0, len(groupSessionIDs)+1)
		for _, id := range groupSessionIDs {
			args = append(args, id)
		}
		args = append(args, startOfDay(tz).UTC().Format(time.RFC3339Nano))
		coverage, err := budgetCoverageTx(tx, where, args...)
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget coverage query failed"}, err
		}
		worst = applyBudgetCoverage(worst, coverage, total, limit)
	}

	return worst, nil
}

// Check is a convenience for non-transactional checks (e.g., TUI display).
func (b *BudgetChecker) Check(sessionID, groupName string) BudgetResult {
	worst := BudgetResult{Action: BudgetActionNone}

	if b.cfg.DailyLimit > 0 {
		summary, _ := b.store.CoveredTotalToday()
		r := evaluate(summary.TotalCostMicrodollars, b.cfg.DailyLimit, "daily global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		worst = applyBudgetCoverage(worst, summary.Coverage, summary.TotalCostMicrodollars, b.cfg.DailyLimit)
	}

	if b.cfg.WeeklyLimit > 0 {
		summary, _ := b.store.CoveredTotalThisWeek()
		r := evaluate(summary.TotalCostMicrodollars, b.cfg.WeeklyLimit, "weekly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		worst = applyBudgetCoverage(worst, summary.Coverage, summary.TotalCostMicrodollars, b.cfg.WeeklyLimit)
	}

	if b.cfg.MonthlyLimit > 0 {
		summary, _ := b.store.CoveredTotalThisMonth()
		r := evaluate(summary.TotalCostMicrodollars, b.cfg.MonthlyLimit, "monthly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
		worst = applyBudgetCoverage(worst, summary.Coverage, summary.TotalCostMicrodollars, b.cfg.MonthlyLimit)
	}

	return worst
}

func evaluate(used, limit int64, reason string) BudgetResult {
	if limit <= 0 {
		return BudgetResult{Action: BudgetActionNone}
	}
	pct := float64(used) / float64(limit)
	if pct >= 1.0 {
		return BudgetResult{Action: BudgetActionStop, Reason: reason, UsedMicro: used, LimitMicro: limit, Percentage: pct * 100}
	}
	if pct >= 0.8 {
		return BudgetResult{Action: BudgetActionWarn, Reason: reason, UsedMicro: used, LimitMicro: limit, Percentage: pct * 100}
	}
	return BudgetResult{Action: BudgetActionNone, UsedMicro: used, LimitMicro: limit, Percentage: pct * 100}
}

func applyBudgetCoverage(result BudgetResult, coverage Coverage, used, limit int64) BudgetResult {
	if coverage.UnknownPriceEventCount == 0 && coverage.UnreconciledEventCount == 0 {
		return result
	}
	result.CoverageIncomplete = true
	if coverage.UnknownPriceTokens > result.UnknownPriceTokens {
		result.UnknownPriceTokens = coverage.UnknownPriceTokens
	}
	if coverage.UnreconciledTokens > result.UnreconciledTokens {
		result.UnreconciledTokens = coverage.UnreconciledTokens
	}
	if result.Action == BudgetActionNone {
		result.Action = BudgetActionWarn
		result.Reason = "budget price coverage incomplete"
		result.UsedMicro = used
		result.LimitMicro = limit
	}
	return result
}

func budgetCoverageTx(tx *sql.Tx, where string, args ...any) (Coverage, error) {
	tokens := `(input_tokens + cache_read_tokens + cache_write_tokens + output_tokens)`
	query := `SELECT
		COALESCE(SUM(CASE WHEN pricing_status = ? THEN ` + tokens + ` ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN pricing_status = ? OR reconciliation_status = ? THEN ` + tokens + ` ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN pricing_status = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN pricing_status = ? OR reconciliation_status = ? THEN 1 ELSE 0 END), 0)
		FROM cost_events WHERE ` + where + ` AND reconciliation_status <> ?`
	queryArgs := []any{
		PricingUnknown, PricingLegacyUnresolved, ReconciliationLegacyUnreconciled,
		PricingUnknown, PricingLegacyUnresolved, ReconciliationLegacyUnreconciled,
	}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, ReconciliationLegacySuperseded)
	var coverage Coverage
	err := tx.QueryRow(query, queryArgs...).Scan(
		&coverage.UnknownPriceTokens, &coverage.UnreconciledTokens,
		&coverage.UnknownPriceEventCount, &coverage.UnreconciledEventCount)
	return coverage, err
}

func startOfDay(tz *time.Location) time.Time {
	now := time.Now().In(tz)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz)
}

func startOfWeek(tz *time.Location) time.Time {
	now := time.Now().In(tz)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday = 7
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, tz)
}

func startOfMonth(tz *time.Location) time.Time {
	now := time.Now().In(tz)
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tz)
}
