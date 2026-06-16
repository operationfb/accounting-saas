import { z } from 'zod'

// A project as returned by the projects API. Mirrors the Go ProjectResponse
// (server.go): money arrives as decimal POUND strings (e.g. "75.00") — never do
// arithmetic on them, just format for display — times as "H:MM", dates as
// "YYYY-MM-DD", timestamps as RFC3339. Nullable fields use .nullish() so both an
// explicit `null` and an omitted key validate.
export const ProjectSchema = z.object({
  id: z.string(),
  organisation_id: z.string(),
  // The project's client. The list API returns only this id (not the contact's
  // name), so the view joins to the contacts list to show a readable name.
  contact_id: z.string(),

  // Core
  name: z.string(),
  status: z.string(), // active | inactive | completed | cancelled
  contract_po_number: z.string().nullish(),
  project_invoice_sequence: z.boolean(),
  currency: z.string(),

  // Budget — at most one of these is populated, selected by budget_type.
  budget_type: z.string().nullish(), // hours | days | money
  budget_hours: z.string().nullish(), // "H:MM" when budget_type = hours
  budget_days: z.number().nullish(), // whole days when budget_type = days
  budget_money: z.string().nullish(), // pound string when budget_type = money

  // Billing
  hours_per_day: z.string().nullish(), // "H:MM" working day length
  billing_rate: z.string(), // pound string, "0.00" when the user left it unset
  billing_rate_unit: z.string().nullish(), // per_hour | per_day
  billing_rate_plus_vat: z.boolean(),

  // More options
  is_ir35: z.boolean(),
  start_date: z.string().nullish(), // "YYYY-MM-DD"
  end_date: z.string().nullish(), // "YYYY-MM-DD"
  include_unbillable_time: z.boolean(),

  created_at: z.string(),
  updated_at: z.string(),
})
export type Project = z.infer<typeof ProjectSchema>

// GET /api/v1/projects → { projects: [...] }. An empty list may arrive as null
// (Go marshals a nil slice to null), so the service defaults it to [].
export const ListProjectsResponseSchema = z.object({
  projects: z.array(ProjectSchema).nullish(),
})
