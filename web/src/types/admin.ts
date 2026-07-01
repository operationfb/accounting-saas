import { z } from 'zod'

// Types for the platform-admin ("god view") endpoints (GET /api/v1/admin/*).
// These mirror the backend platformadmin DTOs. Read-only, cross-tenant — only a
// superuser (auth.user.is_superuser) ever sees them.

export const AdminOrganisationSchema = z.object({
  id: z.string(),
  name: z.string(),
  country_code: z.string(),
  plan: z.string(),
  member_count: z.number(),
  created_at: z.string(),
})
export type AdminOrganisation = z.infer<typeof AdminOrganisationSchema>

// Body for POST /admin/organisations (superuser create). Minimal: name + the two
// creation-time immutable fields. Everything else is set afterward on Company
// Details. The backend provisions the chart of accounts on create.
export interface CreateOrganisationRequest {
  name: string
  country_code: string
  native_currency: string
}

// Body for POST /admin/organisations/:id/members — attach an existing user.
export interface AddOrganisationMemberRequest {
  user_id: string
  role: string
}

// Body for POST /admin/organisations/:id/users — create a new user under the org.
export interface CreateOrganisationUserRequest {
  email: string
  password: string
  first_name: string
  last_name: string
  role: string
}

export const AdminUserSchema = z.object({
  id: z.string(),
  email: z.string(),
  first_name: z.string(),
  last_name: z.string(),
  is_active: z.boolean(),
  is_superuser: z.boolean(),
  last_login_at: z.string().nullish(),
  created_at: z.string(),
})
export type AdminUser = z.infer<typeof AdminUserSchema>

// A user's membership row on the user drill-in (which orgs they belong to).
export const AdminMembershipSchema = z.object({
  organisation_id: z.string(),
  organisation_name: z.string(),
  role: z.string(),
  status: z.string(),
  member_since: z.string(),
})
export type AdminMembership = z.infer<typeof AdminMembershipSchema>

// A member row on the org drill-in (a user in that org).
export const AdminOrganisationMemberSchema = z.object({
  user_id: z.string(),
  email: z.string(),
  first_name: z.string(),
  last_name: z.string(),
  role: z.string(),
  status: z.string(),
  last_login_at: z.string().nullish(),
  member_since: z.string(),
})
export type AdminOrganisationMember = z.infer<typeof AdminOrganisationMemberSchema>

export const AdminOrganisationsResponseSchema = z.object({
  organisations: z.array(AdminOrganisationSchema),
})

export const AdminUsersResponseSchema = z.object({
  users: z.array(AdminUserSchema),
})

// The org drill-in: the org summary + its members (backs the per-org user list).
export const AdminOrganisationDetailSchema = z.object({
  organisation: AdminOrganisationSchema,
  members: z.array(AdminOrganisationMemberSchema),
})
export type AdminOrganisationDetail = z.infer<typeof AdminOrganisationDetailSchema>

export const AdminUserDetailSchema = z.object({
  user: AdminUserSchema,
  memberships: z.array(AdminMembershipSchema),
})
export type AdminUserDetail = z.infer<typeof AdminUserDetailSchema>
