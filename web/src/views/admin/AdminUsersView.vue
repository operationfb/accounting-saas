<script setup lang="ts">
// Platform-admin ("god view") — all users across every tenant. Superuser only
// (API 403s a normal user; the guard + load() redirect otherwise). Read-only.
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import AppLayout from '@/layouts/AppLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { listAllUsers } from '@/services/admin.service'
import type { AdminUser } from '@/types/admin'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const auth = useAuthStore()

const users = ref<AdminUser[]>([])
const loading = ref(true)
const error = ref('')

function displayName(u: AdminUser): string {
  return `${u.first_name} ${u.last_name}`.trim() || u.email
}

function formatLastLogin(iso?: string | null): string {
  if (!iso) return 'Never'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const date = d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: '2-digit' })
  const time = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  return `${date} at ${time}`
}

async function load() {
  if (!auth.user?.is_superuser) {
    router.replace('/dashboards')
    return
  }
  loading.value = true
  error.value = ''
  try {
    users.value = await listAllUsers()
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
    <div class="mb-[18px]">
      <h1 class="text-[22px] font-bold">Platform Admin</h1>
      <p class="text-sm text-fa-muted">Read-only view across all organisations and users.</p>
    </div>

    <div class="mb-4 flex gap-2 border-b border-fa-border">
      <RouterLink
        to="/admin/organisations"
        class="border-b-2 px-3 py-2 text-sm font-semibold"
        :class="$route.path.startsWith('/admin/organisations') ? 'border-fa-green text-fa-text' : 'border-transparent text-fa-muted'"
      >
        Organisations
      </RouterLink>
      <RouterLink
        to="/admin/users"
        class="border-b-2 px-3 py-2 text-sm font-semibold"
        :class="$route.path.startsWith('/admin/users') ? 'border-fa-green text-fa-text' : 'border-transparent text-fa-muted'"
      >
        Users
      </RouterLink>
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading users…
      </div>
      <div v-else-if="error" class="px-4 py-12 text-center text-sm text-[#c0392b]">{{ error }}</div>
      <div v-else-if="users.length === 0" class="px-4 py-14 text-center font-semibold">No users</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Name</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Email</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Status</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Last logged in</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="u in users"
              :key="u.id"
              class="cursor-pointer hover:bg-[#f7fafc]"
              @click="router.push(`/admin/users/${u.id}`)"
            >
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <span class="font-semibold text-fa-blue hover:underline">{{ displayName(u) }}</span>
                <span
                  v-if="u.is_superuser"
                  class="ml-2 rounded bg-[#fdecea] px-1.5 py-0.5 text-xs font-semibold text-[#c0392b]"
                  >Superuser</span
                >
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ u.email }}</td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <span
                  class="rounded px-1.5 py-0.5 text-xs capitalize"
                  :class="u.is_active ? 'bg-[#e8f5e9] text-[#2e7d32]' : 'bg-[#eef1f4] text-fa-muted'"
                  >{{ u.is_active ? 'Active' : 'Inactive' }}</span
                >
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ formatLastLogin(u.last_login_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </AppLayout>
</template>
