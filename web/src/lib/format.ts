// Display formatters. These are for RENDERING only — the API already gives us
// money as a decimal pound string (e.g. "42.50"); we never do arithmetic on the
// float here, just format it for the screen.

export function formatMoney(amountPounds: string, currency = 'GBP'): string {
  const n = Number(amountPounds)
  if (Number.isNaN(n)) return amountPounds
  try {
    return new Intl.NumberFormat('en-GB', { style: 'currency', currency }).format(n)
  } catch {
    // Unknown currency code → fall back to a plain prefix.
    return `${currency} ${amountPounds}`
  }
}

export function formatDate(iso: string): string {
  // iso is "YYYY-MM-DD". Parse as LOCAL midnight (T00:00:00) so the displayed
  // day doesn't shift across timezones.
  const d = new Date(`${iso}T00:00:00`)
  if (Number.isNaN(d.getTime())) return iso
  return new Intl.DateTimeFormat('en-GB', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
  }).format(d)
}

// For RFC3339 timestamps (created_at / updated_at), e.g. "2026-06-11T14:31:54+01:00".
export function formatDateTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return new Intl.DateTimeFormat('en-GB', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(d)
}

// Local YYYY-MM-DD for a Date (used for the `dated_on` payload). Uses the local
// date parts so the day doesn't shift across timezones.
export function toISODate(d: Date): string {
  const year = d.getFullYear()
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

// VAT extracted from a VAT-INCLUSIVE total for a fixed-ratio rate. Mirrors the
// backend's computeFixedVAT exactly: vat = round(grossMinor × rate_bps /
// (10000 + rate_bps)). Math.round is half-up, which equals the backend's
// half-away-from-zero for non-negative amounts. Returns a 2dp pound string, or
// '' when the gross isn't a valid non-negative number.
export function computeFixedVatPounds(grossPounds: string, rateBps: number): string {
  const gross = Number(grossPounds)
  if (!Number.isFinite(gross) || gross < 0) return ''
  const grossMinor = Math.round(gross * 100)
  const denom = 10000 + rateBps
  if (denom <= 0) return ''
  const vatMinor = Math.round((grossMinor * rateBps) / denom)
  return (vatMinor / 100).toFixed(2)
}
