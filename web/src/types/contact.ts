import { z } from 'zod'

// Mirrors the backend's ContactResponse (server.go). A contact is a
// customer/supplier the organisation invoices or buys from. Most fields are
// optional (nullish) because the New Contact form is permissive — a contact may
// be a person, a company, or both. `default_payment_terms_days` is a COUNT OF
// DAYS (0 = "Due on Receipt"), not money, so it stays a number.
export const ContactSchema = z.object({
  id: z.string(),
  organisation_id: z.string(),
  created_by_user_id: z.string(),

  // Contact details
  first_name: z.string().nullish(),
  last_name: z.string().nullish(),
  organisation_name: z.string().nullish(), // the CONTACT's company name, not the tenant
  email: z.string().nullish(),
  billing_email: z.string().nullish(),
  telephone: z.string().nullish(),
  mobile: z.string().nullish(),

  // Invoicing address
  address_line_1: z.string().nullish(),
  address_line_2: z.string().nullish(),
  address_line_3: z.string().nullish(),
  town: z.string().nullish(),
  region: z.string().nullish(),
  postcode: z.string().nullish(),
  country_code: z.string(),

  // Invoicing options
  default_payment_terms_days: z.number().nullish(),
  uses_contact_level_email_settings: z.boolean(),
  uses_contact_level_invoice_sequence: z.boolean(),
  display_contact_name: z.boolean(),
  charge_vat: z.string(), // 'ALWAYS' | 'NEVER' | 'SAME_COUNTRY'
  vat_registration_number: z.string().nullish(),
  invoice_language: z.string(),

  // Bank details
  bank_sort_code: z.string().nullish(),
  bank_account_number: z.string().nullish(),
  bank_recipient_name: z.string().nullish(),

  is_active: z.boolean(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type Contact = z.infer<typeof ContactSchema>

// GET /api/v1/contacts → { "contacts": [...] }. An empty list can come back as
// null (Go marshals a nil slice to null), so allow null and default to [] in the
// service — same convention as ListExpensesResponseSchema.
export const ListContactsResponseSchema = z.object({
  contacts: z.array(ContactSchema).nullish(),
})
