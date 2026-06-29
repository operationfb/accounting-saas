// Payroll period helpers — the UK tax month label and the valid payment-date window.
// A tax month runs from the 6th of one month to the 5th of the next; period 1 begins
// 6 April of the tax-year-start year.

// monthLabel renders a tax-month number as "Month N".
export function monthLabel(period: number): string {
  return `Month ${period}`
}

// periodWindow returns the [start, end] ISO dates of a tax month for a tax year.
export function periodWindow(taxYearStart: number, period: number): { start: string; end: string } {
  // 6 April of the start year, then (period-1) months on.
  const start = new Date(Date.UTC(taxYearStart, 3 /* April */ + (period - 1), 6))
  // End = one month on, minus a day = the 5th of the following month.
  const end = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth() + 1, 5))
  return { start: iso(start), end: iso(end) }
}

// defaultPaymentDate suggests a sensible payday inside the window (the period end).
export function defaultPaymentDate(taxYearStart: number, period: number): string {
  return periodWindow(taxYearStart, period).end
}

// currentTaxYearStart mirrors the backend: the calendar year if on/after 6 April,
// else the previous year.
export function currentTaxYearStart(now = new Date()): number {
  const y = now.getFullYear()
  const start = new Date(`${y}-04-06T00:00:00`)
  return now < start ? y - 1 : y
}

// taxYearLabel renders "2026/27" from a start year.
export function taxYearLabel(start: number): string {
  return `${start}/${String((start + 1) % 100).padStart(2, '0')}`
}

function iso(d: Date): string {
  return d.toISOString().slice(0, 10)
}
