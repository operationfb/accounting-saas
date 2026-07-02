<script setup lang="ts">
// Platform-admin user drill-in — one user's summary + every org they belong to.
// Superuser only (API 403s otherwise; the guard + load() redirect a non-superuser).
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/layouts/AppLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { getAdminUser } from '@/services/admin.service'
import type { AdminUserDetail } from '@/types/admin'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const detail = ref<AdminUserDetail | null>(null)
const loading = ref(true)
const error = ref('')

async function load() {
  if (!auth.user?.is_superuser) {
    router.replace('/dashboards')
    return
  }
  loading.value = true
  error.value = ''
  try {
    detail.value = await getAdminUser(String(route.params.id))
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load user.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <RouterLink to="/admin/users" class="mb-3 inline-block text-sm text-fa-blue hover:underline">
      ← All users
    </RouterLink>

    <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading…
    </div>
    <div v-else-if="error" class="px-4 py-12 text-sm text-[#c0392b]">{{ error }}</div>
    <template v-else-if="detail">
      <h1 class="mb-1 text-[22px] font-bold">
        {{ `${detail.user.first_name} ${detail.user.last_name}`.trim() || detail.user.email }}
        <span
          v-if="detail.user.is_superuser"
          class="ml-2 align-middle rounded bg-[#fdecea] px-1.5 py-0.5 text-xs font-semibold text-[#c0392b]"
          >Superuser</span
        >
      </h1>
      <p class="mb-5 text-sm text-fa-muted">
        {{ detail.user.email }} · {{ detail.user.is_active ? 'Active' : 'Inactive' }}
      </p>

      <h2 class="mb-2 text-[15px] font-semibold">Organisations</h2>
      <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
        <div v-if="detail.memberships.length === 0" class="px-4 py-10 text-center text-fa-muted">
          Belongs to no organisation
        </div>
        <table v-else class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Organisation</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Role</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Status</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="m in detail.memberships"
              :key="m.organisation_id"
              class="cursor-pointer hover:bg-[#f7fafc]"
              @click="router.push(`/admin/organisations/${m.organisation_id}/company-details`)"
            >
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle font-semibold text-fa-blue hover:underline">
                {{ m.organisation_name }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle capitalize text-fa-muted">{{ m.role }}</td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle capitalize text-fa-muted">{{ m.status }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </AppLayout>
</template>
