import { z } from 'zod'

// GET /api/v1/currencies → { "currencies": [...] } — the global ISO 4217
// reference list (155 rows, ordered by code). `symbol` may be null (no
// well-known glyph); `minor_unit` is the number of decimal digits the currency
// uses (2 for most, 0 for JPY, 3 for the Gulf dinars).
export const CurrencySchema = z.object({
  code: z.string(),
  name: z.string(),
  symbol: z.string().nullish(),
  minor_unit: z.number(),
})
export type Currency = z.infer<typeof CurrencySchema>

export const ListCurrenciesResponseSchema = z.object({
  currencies: z.array(CurrencySchema).nullish(),
})
