package vat

// periods.go
// =============================================================================
// VAT period generation — the schedule of return periods derived from the org's
// VAT settings (effective date, first-return period end, frequency), with no HMRC
// connection yet. When MTD is wired in Phase 2 the period list comes from HMRC's
// /obligations instead; this local generation is the v1 stand-in.
//
// Pure + deterministic — it takes `today` as a parameter rather than reading the
// clock — so it unit-tests without a DB (the first pure logic in this package).
// =============================================================================

import "time"

// vatPeriod is one generated return period (dates only; the DTO layer adds the
// label + status).
type vatPeriod struct {
	Start time.Time
	End   time.Time
	Due   time.Time
}

// frequencyMonths maps the return frequency to the number of calendar months a
// REGULAR period spans. 0 = unknown frequency (caller treats it as "can't generate").
func frequencyMonths(frequency string) int {
	switch frequency {
	case "monthly":
		return 1
	case "quarterly":
		return 3
	case "annually":
		return 12
	default:
		return 0
	}
}

// vatDueDate returns the MTD filing deadline for a period ending on `end`: the 7th
// day of the SECOND month after the period-end month (e.g. a period ending 31 May
// is due 7 Jul). This is the standard "one calendar month and 7 days" deadline,
// expressed in a way that is exact for month-end periods — and VAT periods always
// end on a month end. NOTE: the Annual Accounting Scheme's 2-month deadline is a
// future refinement (see BACKLOG); v1 applies the standard rule to all frequencies.
func vatDueDate(end time.Time) time.Time {
	year, month := end.Year(), int(end.Month())+2
	for month > 12 {
		month -= 12
		year++
	}
	return time.Date(year, time.Month(month), 7, 0, 0, 0, 0, time.UTC)
}

// generateVATPeriods builds the period schedule from the certificate settings up to
// (and including) the period in progress on `today`. The FIRST period runs from the
// effective date to the first-return period end — it can be irregular (registration
// mid-quarter) — and every subsequent period is a regular `frequency`-long span
// starting the day after the previous period's end. Periods are returned oldest
// first. Returns nil when the settings are incomplete or inconsistent (unknown
// frequency, or first end before the effective date). The loop cap is a safety
// valve against a wildly past effective date.
func generateVATPeriods(effective, firstEnd, today time.Time, frequency string) []vatPeriod {
	months := frequencyMonths(frequency)
	if months == 0 {
		return nil
	}

	// Normalise to a UTC-midnight date so all comparisons are by calendar day.
	effective = dateOnlyUTC(effective)
	firstEnd = dateOnlyUTC(firstEnd)
	today = dateOnlyUTC(today)

	if firstEnd.Before(effective) {
		return nil
	}

	var periods []vatPeriod
	start, end := effective, firstEnd
	for i := 0; i < 600; i++ { // ~50 years monthly — defends against bad data
		if start.After(today) {
			break // this period hasn't started yet
		}
		periods = append(periods, vatPeriod{Start: start, End: end, Due: vatDueDate(end)})
		// Next period: starts the day after this end (a month-end → the 1st), and
		// spans `months` calendar months. Starting on the 1st keeps AddDate exact.
		start = end.AddDate(0, 0, 1)
		end = start.AddDate(0, months, 0).AddDate(0, 0, -1)
	}
	return periods
}

// dateOnlyUTC strips the clock component, keeping the calendar date at UTC midnight.
func dateOnlyUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
