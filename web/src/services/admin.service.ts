import { apiFetch } from '@/lib/api'
import {
  AdminOrganisationDetailSchema,
  AdminOrganisationSchema,
  AdminOrganisationsResponseSchema,
  AdminUserDetailSchema,
  AdminUsersResponseSchema,
  type AddOrganisationMemberRequest,
  type AdminOrganisation,
  type AdminOrganisationDetail,
  type AdminUser,
  type AdminUserDetail,
  type CreateOrganisationRequest,
  type CreateOrganisationUserRequest,
} from '@/types/admin'
import {
  GetOrganisationResponseSchema,
  type OrganisationDetails,
  type UpdateOrganisationRequest,
} from '@/types/organisation'

// Platform-admin ("god view") API — read-only, cross-tenant. Every endpoint is
// superuser-gated on the backend (403 otherwise), so only call these when
// auth.user.is_superuser is true. The bearer token + 401 handling come from
// apiFetch, like every other service.

// GET /api/v1/admin/organisations — every organisation on the platform.
export async function listAllOrganisations(): Promise<AdminOrganisation[]> {
  const data = await apiFetch<unknown>('/admin/organisations')
  return AdminOrganisationsResponseSchema.parse(data).organisations
}

// POST /api/v1/admin/organisations — create a new org (and provision its chart of
// accounts, server-side). Superuser only. Returns the created org summary.
export async function createAdminOrganisation(
  payload: CreateOrganisationRequest,
): Promise<AdminOrganisation> {
  const data = await apiFetch<{ organisation: unknown }>('/admin/organisations', {
    method: 'POST',
    body: payload,
  })
  return AdminOrganisationSchema.parse(data.organisation)
}

// GET /api/v1/admin/organisations/:id — one org + its members (the per-org user
// list). Superuser only.
export async function getAdminOrganisation(id: string): Promise<AdminOrganisationDetail> {
  const data = await apiFetch<unknown>(`/admin/organisations/${id}`)
  return AdminOrganisationDetailSchema.parse(data)
}

// POST /api/v1/admin/organisations/:id/members — attach an existing user to the
// org. Superuser only. Returns the refreshed org detail (org + members).
export async function addAdminOrganisationMember(
  id: string,
  payload: AddOrganisationMemberRequest,
): Promise<AdminOrganisationDetail> {
  const data = await apiFetch<unknown>(`/admin/organisations/${id}/members`, {
    method: 'POST',
    body: payload,
  })
  return AdminOrganisationDetailSchema.parse(data)
}

// POST /api/v1/admin/organisations/:id/users — create a new user under the org.
// Superuser only. Returns the refreshed org detail (org + members).
export async function createAdminOrganisationUser(
  id: string,
  payload: CreateOrganisationUserRequest,
): Promise<AdminOrganisationDetail> {
  const data = await apiFetch<unknown>(`/admin/organisations/${id}/users`, {
    method: 'POST',
    body: payload,
  })
  return AdminOrganisationDetailSchema.parse(data)
}

// GET /api/v1/admin/users — every user on the platform.
export async function listAllUsers(): Promise<AdminUser[]> {
  const data = await apiFetch<unknown>('/admin/users')
  return AdminUsersResponseSchema.parse(data).users
}

// GET /api/v1/admin/users/:id — one user + the orgs they belong to.
export async function getAdminUser(id: string): Promise<AdminUserDetail> {
  const data = await apiFetch<unknown>(`/admin/users/${id}`)
  return AdminUserDetailSchema.parse(data)
}

// GET /api/v1/admin/organisations/:id/company-details — a chosen org's full
// company details (same shape as the self GET /organisation, so it reuses the
// same schema + types). Superuser only.
export async function getAdminOrganisationDetails(id: string): Promise<OrganisationDetails> {
  const data = await apiFetch<unknown>(`/admin/organisations/${id}/company-details`)
  return GetOrganisationResponseSchema.parse(data).organisation
}

// PUT /api/v1/admin/organisations/:id/company-details — edit a chosen org's
// company details. Superuser only; country_code/native_currency stay immutable
// (same request DTO as the self PUT). Returns the updated record.
export async function updateAdminOrganisationDetails(
  id: string,
  payload: UpdateOrganisationRequest,
): Promise<OrganisationDetails> {
  const data = await apiFetch<unknown>(`/admin/organisations/${id}/company-details`, {
    method: 'PUT',
    body: payload,
  })
  return GetOrganisationResponseSchema.parse(data).organisation
}
