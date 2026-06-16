import { apiFetch } from '@/lib/api'
import { ListMembersResponseSchema, type OrganisationMember } from '@/types/member'

// GET /api/v1/members — every member of the caller's organisation. This endpoint
// is OWNER/ADMIN-ONLY (a plain member gets 403), so only call it when
// auth.isOrgAdmin is true. It returns members of all statuses; callers filter to
// 'active' where needed (e.g. the claimant picker). The bearer token and 401
// handling come from apiFetch, exactly like expenses.service.
export async function listMembers(): Promise<OrganisationMember[]> {
  const data = await apiFetch<unknown>('/members', { method: 'GET' })
  return ListMembersResponseSchema.parse(data).members ?? []
}
