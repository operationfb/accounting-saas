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

// GET /:id, POST, and PUT all return a single project as { project: {...} }.
export const GetProjectResponseSchema = z.object({
  project: ProjectSchema,
})

// Body for POST /api/v1/projects (create) and PUT /api/v1/projects/:id (update) —
// the two share the same shape. Mirrors the Go CreateProjectRequest: only
// contact_id and name are required; omit an optional field to leave it at the
// backend default. Money/budget amounts are POUND strings and times are "H:MM"
// (or decimal-hours) strings — the backend parses + converts them to pence/minutes.
export interface CreateProjectRequest {
  contact_id: string
  name: string
  status?: string
  contract_po_number?: string
  project_invoice_sequence?: boolean
  currency?: string
  // Budget: send budget_type plus the ONE matching amount; omit all for "no budget".
  budget_type?: string // hours | days | money
  budget_hours?: string // "H:MM" or decimal hours
  budget_days?: number
  budget_money?: string // pound string
  hours_per_day?: string // "H:MM" or decimal hours
  billing_rate?: string // pound string
  billing_rate_unit?: string // per_hour | per_day
  billing_rate_plus_vat?: boolean
  is_ir35?: boolean
  start_date?: string // "YYYY-MM-DD"
  end_date?: string // "YYYY-MM-DD"
  include_unbillable_time?: boolean
}
