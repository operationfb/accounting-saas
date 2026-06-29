import { apiFetch } from '@/lib/api'
import {
  ListMembersResponseSchema,
  MemberDetailSchema,
  type CreateMemberRequest,
  type MemberDetail,
  type OrganisationMember,
  type UpdateMemberRequest,
} from '@/types/member'
import { InboxAddressSchema } from '@/types/user'

// GET /api/v1/members — every member of the caller's organisation. This endpoint
// is OWNER/ADMIN-ONLY (a plain member gets 403), so only call it when
// auth.isOrgAdmin is true. It returns members of all statuses; callers filter to
// 'active' where needed (e.g. the claimant picker). The bearer token and 401
// handling come from apiFetch, exactly like expenses.service.
export async function listMembers(): Promise<OrganisationMember[]> {
  const data = await apiFetch<unknown>('/members', { method: 'GET' })
  return ListMembersResponseSchema.parse(data).members ?? []
}

// POST /api/v1/members — create a new user and add them to the organisation with
// an initial password. OWNER/ADMIN-ONLY (403 otherwise); a duplicate email is 409.
// Returns the created member's full detail (same shape as getMember).
export async function createMember(payload: CreateMemberRequest): Promise<MemberDetail> {
  const data = await apiFetch<unknown>('/members', { method: 'POST', body: payload })
  return MemberDetailSchema.parse(data)
}

// GET /api/v1/members/:id — one member's full detail (profile + payroll +
// role/status) for the admin User Details screen. OWNER/ADMIN-ONLY (403
// otherwise); a member of another org 404s.
export async function getMember(id: string): Promise<MemberDetail> {
  const data = await apiFetch<unknown>(`/members/${id}`, { method: 'GET' })
  return MemberDetailSchema.parse(data)
}

// PUT /api/v1/members/:id — update another user's details, role and status.
// OWNER/ADMIN-ONLY. Returns the updated detail.
export async function updateMember(id: string, payload: UpdateMemberRequest): Promise<MemberDetail> {
  const data = await apiFetch<unknown>(`/members/${id}`, { method: 'PUT', body: payload })
  return MemberDetailSchema.parse(data)
}

// GET /api/v1/members/:id/inbox-address — a target member's Mailgun receipt-inbox
// address, for an owner/admin viewing them on the User Details screen.
// OWNER/ADMIN-ONLY. `enabled` is false when the channel isn't configured.
export async function getMemberInboxAddress(id: string): Promise<{ enabled: boolean; address: string }> {
  const data = await apiFetch<unknown>(`/members/${id}/inbox-address`, { method: 'GET' })
  return InboxAddressSchema.parse(data)
}
