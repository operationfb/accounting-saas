<script setup lang="ts">
// Platform-admin ("god view") — all organisations across every tenant. Superuser
// only: the API 403s a normal user, and the router guard + the load() belt-and-
// braces redirect anyone without auth.user.is_superuser away. Read-only.
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import CreateOrganisationDialog from '@/components/CreateOrganisationDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { listAllOrganisations } from '@/services/admin.service'
import type { AdminOrganisation } from '@/types/admin'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const auth = useAuthStore()

const orgs = ref<AdminOrganisation[]>([])
const loading = ref(true)
const error = ref('')
const showCreate = ref(false)

// After a create, drop the superuser on the new org's Company Details so they can
// finish setup (company type, address, VAT). The list refreshes when they return.
function onCreated(org: AdminOrganisation) {
  router.push(`/admin/organisations/${org.id}/company-details`)
}

function formatDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime())
    ? '—'
    : d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' })
}

async function load() {
  if (!auth.user?.is_superuser) {
    router.replace('/overview')
    return
  }
  loading.value = true
  error.value = ''
  try {
    orgs.value = await listAllOrganisations()
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load organisations.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="text-[22px] font-bold">Platform Admin</h1>
        <p class="text-sm text-fa-muted">Manage all organisations and users across the platform.</p>
      </div>
      <Button label="Create Organisation" icon="pi pi-plus" @click="showCreate = true" />
    </div>

    <CreateOrganisationDialog v-model:visible="showCreate" @created="onCreated" />

    <!-- Sub-nav between the two god-view screens. -->
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
        <i class="pi pi-spin pi-spinner mr-2" />Loading organisations…
      </div>
      <div v-else-if="error" class="px-4 py-12 text-center text-sm text-[#c0392b]">{{ error }}</div>
      <div v-else-if="orgs.length === 0" class="px-4 py-14 text-center font-semibold">No organisations</div>
      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Name</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Country</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Plan</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Members</th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Created</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="o in orgs" :key="o.id" class="hover:bg-[#f7fafc]">
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <!-- Company name → that org's Company Details (view/edit). -->
                <RouterLink
                  :to="`/admin/organisations/${o.id}/company-details`"
                  class="font-semibold text-fa-blue hover:underline"
                >
                  {{ o.name }}
                </RouterLink>
                <!-- Separate link to that org's user list. -->
                <RouterLink
                  :to="`/admin/organisations/${o.id}`"
                  class="ml-3 text-xs text-fa-muted hover:text-fa-blue hover:underline"
                >
                  Users
                </RouterLink>
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ o.country_code }}</td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle capitalize text-fa-muted">{{ o.plan }}</td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ o.member_count }}</td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ formatDate(o.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </AppLayout>
</template>
