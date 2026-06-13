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
