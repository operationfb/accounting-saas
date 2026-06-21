import { apiFetch } from '@/lib/api'
import { ListCurrenciesResponseSchema, type Currency } from '@/types/currency'

// GET /api/v1/currencies — the global ISO 4217 currency list that populates the
// currency pickers. The bearer token is attached by apiFetch, and a 401 is
// handled there (logout + redirect). Mirrors listCategories / listVatRates.
export async function listCurrencies(): Promise<Currency[]> {
  const data = await apiFetch<unknown>('/currencies', { method: 'GET' })
  return ListCurrenciesResponseSchema.parse(data).currencies ?? []
}
