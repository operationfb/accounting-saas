<script setup lang="ts">
// Platform-admin per-org USER LIST — one organisation's members. Reached from the
// "Users" link on the org list. Superuser only (API 403s otherwise; the guard +
// load() redirect a non-superuser). A header link jumps to the org's Company Details.
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import AddOrganisationMemberDialog from '@/components/AddOrganisationMemberDialog.vue'
import CreateOrganisationUserDialog from '@/components/CreateOrganisationUserDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { getAdminOrganisation } from '@/services/admin.service'
import type { AdminOrganisationDetail } from '@/types/admin'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const detail = ref<AdminOrganisationDetail | null>(null)
const loading = ref(true)
const error = ref('')

const showAdd = ref(false)
const showCreate = ref(false)
const orgId = computed(() => String(route.params.id))
// A brand-new org has no members, so default the first user to 'owner'.
const defaultRole = computed(() => (detail.value && detail.value.members.length === 0 ? 'owner' : 'member'))

// Both dialogs return the refreshed org detail; swap it in so the list updates.
function onChanged(updated: AdminOrganisationDetail) {
  detail.value = updated
}

function displayName(m: { first_name: string; last_name: string; email: string }): string {
  return `${m.first_name} ${m.last_name}`.trim() || m.email
}

async function load() {
  if (!auth.user?.is_superuser) {
    router.replace('/overview')
    return
  }
  loading.value = true
  error.value = ''
  try {
    detail.value = await getAdminOrganisation(String(route.params.id))
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load organisation.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <RouterLink to="/admin/organisations" class="mb-3 inline-block text-sm text-fa-blue hover:underline">
      ← All organisations
    </RouterLink>

    <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading…
    </div>
    <div v-else-if="error" class="px-4 py-12 text-sm text-[#c0392b]">{{ error }}</div>
    <template v-else-if="detail">
      <div class="mb-1 flex flex-wrap items-center justify-between gap-2">
        <h1 class="text-[22px] font-bold">{{ detail.organisation.name }}</h1>
        <RouterLink
          :to="`/admin/organisations/${detail.organisation.id}/company-details`"
          class="text-sm text-fa-blue hover:underline"
        >
          Company details →
        </RouterLink>
      </div>
      <p class="mb-5 text-sm text-fa-muted">
        {{ detail.organisation.country_code }} · <span class="capitalize">{{ detail.organisation.plan }}</span> ·
        {{ detail.organisation.member_count }} member(s)
      </p>

      <div class="mb-2 flex items-center justify-between gap-3">
        <h2 class="text-[15px] font-semibold">Users</h2>
        <div class="flex items-center gap-2">
          <Button label="Add users" icon="pi pi-user-plus" severity="secondary" outlined size="small" @click="showAdd = true" />
          <Button label="Create User" icon="pi pi-plus" size="small" @click="showCreate = true" />
        </div>
      </div>

      <AddOrganisationMemberDialog
        v-model:visible="showAdd"
        :org-id="orgId"
        :default-role="defaultRole"
        @changed="onChanged"
      />
      <CreateOrganisationUserDialog
        v-model:visible="showCreate"
        :org-id="orgId"
        :default-role="defaultRole"
        @changed="onChanged"
      />
      <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
        <div v-if="detail.members.length === 0" class="px-4 py-10 text-center text-fa-muted">No users</div>
        <table v-else class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Name</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Email</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Role</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Status</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="m in detail.members"
              :key="m.user_id"
              class="cursor-pointer hover:bg-[#f7fafc]"
              @click="router.push(`/admin/users/${m.user_id}`)"
            >
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle font-semibold text-fa-blue hover:underline">
                {{ displayName(m) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ m.email }}</td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle capitalize text-fa-muted">{{ m.role }}</td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle capitalize text-fa-muted">{{ m.status }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </AppLayout>
</template>
