// Pure presentation helpers for the currency pickers, shared by the Expense and
// Project entry forms. No API or Vue dependency — just transforms over the
// Currency[] the service returns (see services/currencies.service.ts).
import type { Currency } from '@/types/currency'

// A PrimeVue Select option. `value` is the ISO code stored on the form;
// `disabled` marks the non-selectable dashed separator row.
export interface CurrencyOption {
  label: string
  value: string
  disabled?: boolean
}

// The currencies pinned to the top of every picker, in this order. They also
// remain in the full list below the separator (so scanning the whole list still
// finds them) — matching the reference screenshot's behaviour.
const PRIORITY = ['GBP', 'EUR', 'USD']

// Sentinel value for the separator row. It is disabled (never selectable), and a
// non-3-letter sentinel can't collide with a real ISO 4217 code.
export const CURRENCY_SEPARATOR = '__separator__'

// The dashed separator label shown between the pinned three and the full list.
const SEPARATOR_LABEL = '──────────────'

function toOption(c: Currency): CurrencyOption {
  // "CODE - Name", e.g. "GBP - British Pound".
  return { label: `${c.code} - ${c.name}`, value: c.code }
}

// buildCurrencyOptions turns the API's currency list into picker options: the
// pinned three first, then a disabled dashed separator, then the full list (the
// API already orders it by code). Returns [] for an empty/loading list.
export function buildCurrencyOptions(currencies: Currency[]): CurrencyOption[] {
  if (currencies.length === 0) return []

  const byCode = new Map(currencies.map((c) => [c.code, c]))
  const pinned = PRIORITY.map((code) => byCode.get(code))
    .filter((c): c is Currency => c !== undefined)
    .map(toOption)

  const rest = currencies.map(toOption)

  // Only show the separator once there is actually a pinned group above it.
  if (pinned.length === 0) return rest
  return [...pinned, { label: SEPARATOR_LABEL, value: CURRENCY_SEPARATOR, disabled: true }, ...rest]
}

// currencySymbolMap maps each code to its symbol (e.g. { GBP: '£' }), skipping
// currencies seeded with a null symbol. Used by the expense form's amount addon
// so the £/€/¥ prefix works for any currency, not just the old hardcoded three.
export function currencySymbolMap(currencies: Currency[]): Record<string, string> {
  const map: Record<string, string> = {}
  for (const c of currencies) {
    if (c.symbol) map[c.code] = c.symbol
  }
  return map
}
