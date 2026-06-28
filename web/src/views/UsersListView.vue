<script setup lang="ts">
// Users list — the owner/admin "Users" screen (modelled on FreeAgent's Users
// table). Wired to GET /api/v1/members (owner/admin only). Each row opens the
// unified User Details screen (/users/:id) in admin mode.
//
// Scope: the table is real data. "New User" (inviting a user) is DEFERRED — the
// button is omitted this iteration (see BACKLOG). The "2FA authenticator app"
// column is a faithful but static "Disabled" placeholder — there is no 2FA
// feature yet. A non-admin who reaches this route is redirected to their own
// details (the access gate is enforced both here and by the 403 from the API).
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/layouts/AppLayout.vue'
import Button from 'primevue/button'
import { useAuthStore } from '@/stores/auth'
import { listMembers } from '@/services/members.service'
import type { OrganisationMember } from '@/types/member'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const auth = useAuthStore()

const members = ref<OrganisationMember[]>([])
const loading = ref(true)
const error = ref('')

// Display labels for the access roles. NOTE the deliberate label≠value divergence:
// the `member` access role is shown as "Employee" in the UI; the stored/API value
// stays `member` (don't "fix" this to match).
const roleLabels: Record<string, string> = {
  owner: 'Owner',
  admin: 'Admin',
  member: 'Employee',
  accountant: 'Accountant',
  read_only: 'Read only',
}

function displayName(m: OrganisationMember): string {
  return `${m.first_name} ${m.last_name}`.trim() || m.email
}

function roleLabel(m: OrganisationMember): string {
  return roleLabels[m.role] ?? m.role
}

// "28 Jun 26 at 08:03" style, matching the FA screenshot. Empty when never logged in.
function formatLastLogin(iso?: string | null): string {
  if (!iso) return 'Never'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const date = d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: '2-digit' })
  const time = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  return `${date} at ${time}`
}

function openUser(m: OrganisationMember) {
  router.push(`/users/${m.user_id}`)
}

async function load() {
  // Belt-and-braces: a non-admin shouldn't be here (the API 403s anyway).
  if (!auth.isOrgAdmin) {
    router.replace('/my-details')
    return
  }
  loading.value = true
  error.value = ''
  try {
    members.value = await listMembers()
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load users.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Users</h1>
      <!-- "New User" (invite) is deferred — see BACKLOG. -->
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading users…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty (shouldn't really happen — the caller is at least one member) -->
      <div v-else-if="members.length === 0" class="px-4 py-14 text-center">
        <p class="font-semibold">No users yet</p>
      </div>

      <!-- Data -->
      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Name
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Role
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Email
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                2FA authenticator app
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Last logged in
              </th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="m in members"
              :key="m.user_id"
              class="group cursor-pointer hover:bg-[#f7fafc]"
              @click="openUser(m)"
            >
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <button type="button" class="text-left font-semibold text-fa-blue hover:underline">
                  {{ displayName(m) }}
                </button>
                <span v-if="m.role === 'owner'" class="ml-2 text-fa-muted">(Account Owner)</span>
                <span
                  v-if="m.status !== 'active'"
                  class="ml-2 rounded bg-[#eef1f4] px-1.5 py-0.5 text-xs capitalize text-fa-muted"
                  >{{ m.status }}</span
                >
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ roleLabel(m) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ m.email }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                Disabled
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ formatLastLogin(m.last_login_at) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </AppLayout>
</template>
