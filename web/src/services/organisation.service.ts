import { apiFetch } from '@/lib/api'
import {
  GetOrganisationResponseSchema,
  type OrganisationDetails,
  type UpdateOrganisationRequest,
} from '@/types/organisation'

// GET /api/v1/organisation — the caller's own Company Details. Any ACTIVE member
// may read; the organisation is taken from the bearer token (no id in the path),
// so a caller can only ever read their own org. apiFetch attaches the token and
// handles a 401; a 403 (inactive/non-member) surfaces as an ApiError.
export async function getOrganisation(): Promise<OrganisationDetails> {
  const data = await apiFetch<unknown>('/organisation', { method: 'GET' })
  return GetOrganisationResponseSchema.parse(data).organisation
}

// PUT /api/v1/organisation — update Company Details. OWNER/ADMIN only on the
// backend; the UI also hides the Save button for everyone else. Full replace of
// the form-owned fields; returns the updated record. A 403 (not owner/admin),
// 400 (bad binding) or 422 (bad value, e.g. company_type/country) surfaces as an
// ApiError for the form to display.
export async function updateOrganisation(
  payload: UpdateOrganisationRequest,
): Promise<OrganisationDetails> {
  const data = await apiFetch<unknown>('/organisation', { method: 'PUT', body: payload })
  return GetOrganisationResponseSchema.parse(data).organisation
}
