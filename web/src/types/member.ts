import { z } from 'zod'
import { RoleSchema } from './auth'

// Mirrors the backend MemberResponse (member_service.go): one organisation member
// is a membership row joined to its user. It is the safe public view (no secrets)
// and backs the "Claimant" picker on the expense form (owner/admin only) plus a
// future Team / Manage-users screen.
export const OrganisationMemberSchema = z.object({
  membership_id: z.string(),
  user_id: z.string(),
  email: z.string(),
  first_name: z.string(),
  last_name: z.string(),
  role: RoleSchema,
  // active | invited | suspended | deactivated. Kept as a plain string (not an
  // enum) so a new backend status can't break parsing; callers compare to 'active'.
  status: z.string(),
  avatar_url: z.string().nullish(),
  member_since: z.string(), // RFC3339 (membership created_at)
  last_login_at: z.string().nullish(),
})
export type OrganisationMember = z.infer<typeof OrganisationMemberSchema>

// GET /api/v1/members → { "members": [...] }. A nil slice marshals to null, so
// allow null and default to [] at the call site.
export const ListMembersResponseSchema = z.object({
  members: z.array(OrganisationMemberSchema).nullish(),
})

// Mirrors the backend MemberDetailResponse (internal/members) for the admin User
// Details screen (GET /api/v1/members/:id): the list shape plus the payroll
// fields the detail form edits. Owner/admin only.
export const MemberDetailSchema = OrganisationMemberSchema.extend({
  national_insurance_number: z.string().nullish(),
  utr: z.string().nullish(),
  date_of_birth: z.string().nullish(), // ISO YYYY-MM-DD
})
export type MemberDetail = z.infer<typeof MemberDetailSchema>

// PUT /api/v1/members/:id body. Mirrors the backend UpdateMemberRequest: names
// required, payroll fields optional (null clears), role + status from the fixed
// enums. The service adds the self lock-out and owner-only-owner guards.
export interface UpdateMemberRequest {
  first_name: string
  last_name: string
  national_insurance_number?: string | null
  utr?: string | null
  date_of_birth?: string | null // ISO YYYY-MM-DD
  role: 'owner' | 'admin' | 'member' | 'accountant' | 'read_only'
  status: 'active' | 'suspended' | 'deactivated'
}
