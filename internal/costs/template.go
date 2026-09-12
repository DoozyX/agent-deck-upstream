package costs

import (
	"strconv"
	"strings"
)

// RenderCostLine substitutes {name} placeholders in template with values
// drawn from vars and formatted as USD via FormatUSD. Recognized
// placeholders whose value is non-zero count toward "non-empty"; unknown
// placeholders pass through literally so typos are visible.
//
// When hideWhenZero is true and zero recognized placeholders rendered to
// a non-zero value (either none were recognized at all, or all recognized
// values were zero), the empty string is returned. This preserves the
// "no events, no segment" behavior of the legacy hardcoded status-bar.
//
// The walker is left-to-right and never iterates the vars map, so output
// is deterministic regardless of map iteration order.
func RenderCostLine(template string, vars map[string]int64, hideWhenZero bool) string {
	return renderCostLine(template, vars, nil, hideWhenZero)
}

// RenderCoveredCostLine keeps all legacy cost variables numeric while adding
// coverage/status placeholders for templates that want to explain subtotals.
func RenderCoveredCostLine(template string, vars map[string]int64, coverage Coverage, hideWhenZero bool) string {
	status := "coverage unknown"
	if coverage.CoverageKnown {
		switch {
		case coverage.EventCount > 0 && coverage.KnownPriceEventCount == 0 && (coverage.UnknownPriceEventCount > 0 || coverage.UnreconciledEventCount > 0):
			status = "price unknown"
		case coverage.Complete:
			status = "complete"
		default:
			status = "known subtotal"
		}
	}
	text := map[string]string{
		"coverage_status":     status,
		"unpriced_events":     strconv.Itoa(coverage.UnknownPriceEventCount),
		"unpriced_tokens":     strconv.FormatInt(coverage.UnknownPriceTokens, 10),
		"unreconciled_events": strconv.Itoa(coverage.UnreconciledEventCount),
		"unreconciled_tokens": strconv.FormatInt(coverage.UnreconciledTokens, 10),
	}
	return renderCostLine(template, vars, text, hideWhenZero)
}

func renderCostLine(template string, vars map[string]int64, text map[string]string, hideWhenZero bool) string {
	var b strings.Builder
	b.Grow(len(template))

	hasNonZero := false
	i := 0
	for i < len(template) {
		c := template[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}

		// Found '{'. Scan for matching '}'.
		end := strings.IndexByte(template[i+1:], '}')
		if end < 0 {
			// Unclosed brace, treat the rest as literal.
			b.WriteString(template[i:])
			break
		}
		name := template[i+1 : i+1+end]
		if val, ok := vars[name]; ok {
			b.WriteString(FormatUSD(val))
			if val != 0 {
				hasNonZero = true
			}
		} else if val, ok := text[name]; ok {
			b.WriteString(val)
			if val != "" && val != "0" {
				hasNonZero = true
			}
		} else {
			// Unknown placeholder: pass through literally including braces.
			b.WriteString(template[i : i+2+end])
		}
		i += 2 + end // skip '{' + name + '}'
	}

	if hideWhenZero && !hasNonZero {
		return ""
	}
	return b.String()
}
