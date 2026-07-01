import { z } from 'zod'

// Mirrors the backend's OrganisationDetailsResponse (organisation_service.go).
// This is the organisation's own "Company Details" — the settings screen
// modelled on FreeAgent. It is a SINGLETON resource: the org is taken from the
// bearer token, so there is no list and no id in the URL.
//
// Most fields are optional (nullish) because Company Details is permissive — a
// fresh org may have only a name. `name` and `country_code` always come back. A
// few read-only extras (vrn, native_currency, timezone, plan, …) are returned
// for completeness even though this form does not edit them.
export const OrganisationDetailsSchema = z.object({
  id: z.string(),
  name: z.string(),
  slug: z.string().nullish(),
  legal_name: z.string().nullish(),
  company_type: z.string().nullish(),

  companies_house_number: z.string().nullish(), // "Company Registration Number"
  utr: z.string().nullish(), // "Corporation Tax Reference"
  vrn: z.string().nullish(),
  paye_reference: z.string().nullish(),
  accounts_office_reference: z.string().nullish(),
  claims_employment_allowance: z.boolean(),

  address_line_1: z.string().nullish(),
  address_line_2: z.string().nullish(),
  address_line_3: z.string().nullish(),
  town: z.string().nullish(),
  region: z.string().nullish(),
  postcode: z.string().nullish(),
  country_code: z.string(),

  business_phone: z.string().nullish(),
  contact_email: z.string().nullish(),
  contact_phone: z.string().nullish(),
  website: z.string().nullish(),

  business_category: z.string().nullish(),
  business_description: z.string().nullish(),

  // Read-only extras — surfaced but not editable on this screen.
  native_currency: z.string(),
  timezone: z.string(),
  plan: z.string(),
  is_active: z.boolean(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type OrganisationDetails = z.infer<typeof OrganisationDetailsSchema>

// GET /api/v1/organisation and PUT /api/v1/organisation both return
// { "organisation": {...} }.
export const GetOrganisationResponseSchema = z.object({
  organisation: OrganisationDetailsSchema,
})

// PUT body. Mirrors the backend's UpdateOrganisationRequest (server.go). `name`
// is the only required field (the org's NOT NULL primary name). The rest are
// optional: the form omits empty optional strings, but always sends country_code.
// This is a FULL REPLACE of the form-owned fields — an omitted optional becomes
// NULL — so the view round-trips even fields it doesn't show (e.g. legal_name) to
// avoid wiping them. company_type, when set, must be one of COMPANY_TYPE_OPTIONS.
export interface UpdateOrganisationRequest {
  name: string
  legal_name?: string
  company_type?: string

  companies_house_number?: string
  utr?: string
  paye_reference?: string
  accounts_office_reference?: string
  claims_employment_allowance?: boolean

  address_line_1?: string
  address_line_2?: string
  address_line_3?: string
  town?: string
  region?: string
  postcode?: string
  // country_code + native_currency are intentionally omitted: both are fixed at
  // organisation creation and immutable on this screen (the server preserves them).

  business_phone?: string
  contact_email?: string
  contact_phone?: string
  website?: string

  business_category?: string
  business_description?: string
}

// The "Company type" dropdown. Codes mirror the backend enum (the company_type
// CHECK in auth_schema.sql and validCompanyType in organisation_service.go);
// labels are the UK company types shown to the user. `partnership` covers LLPs
// too (hence the "Partnership / LLP" label).
export const COMPANY_TYPE_OPTIONS: { label: string; value: string }[] = [
  { label: 'Limited Company', value: 'limited' },
  { label: 'Sole Trader', value: 'sole_trader' },
  { label: 'Partnership / LLP', value: 'partnership' },
  { label: 'Unincorporated Landlord', value: 'landlord' },
  { label: 'Corporation', value: 'corporation' },
]
