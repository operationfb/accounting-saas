package payroll

// periods.go
// =============================================================================
// UK tax-year / tax-month arithmetic. The tax year is identified by the calendar
// year it STARTS in (2026 = the 2026/27 year, 6 Apr 2026 → 5 Apr 2027). A tax MONTH
// (period) runs from the 6th of one month to the 5th of the next:
//   period 1  = 6 Apr → 5 May, period 2 = 6 May → 5 Jun, … period 12 = 6 Mar → 5 Apr.
// =============================================================================

import "time"

// currentTaxYearStart returns the tax-year-start for the given instant: the calendar
// year if we're on/after 6 April, otherwise the previous year.
func currentTaxYearStart(now time.Time) int {
	y := now.Year()
	start := time.Date(y, time.April, 6, 0, 0, 0, 0, time.UTC)
	if now.Before(start) {
		return y - 1
	}
	return y
}

// taxYearLabel renders "2026/27" from a start year.
func taxYearLabel(start int) string {
	end := (start + 1) % 100
	return itoa(start) + "/" + pad2(end)
}

// periodWindow returns the [start, end] dates (inclusive) of a tax month.
func periodWindow(taxYearStart, period int) (start, end time.Time) {
	// Period 1 begins 6 April of the start year; each later period is one month on.
	start = time.Date(taxYearStart, time.April, 6, 0, 0, 0, 0, time.UTC).AddDate(0, period-1, 0)
	// End is the 5th of the following month = one month on, minus a day.
	end = start.AddDate(0, 1, -1)
	return start, end
}

// dateInWindow reports whether d falls within [start, end] inclusive (date-only).
func dateInWindow(d, start, end time.Time) bool {
	day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	return !day.Before(start) && !day.After(end)
}

// itoa / pad2 avoid importing strconv just for two tiny conversions.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}
