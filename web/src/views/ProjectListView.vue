<script setup lang="ts">
// Projects list — the "projects view". Wired to GET /api/v1/projects.
// Mirrors ContactListView: AppLayout wrapper, PrimeVue Select/Button, a
// hand-rolled Tailwind table with the fa-* theme colours, and a
// loading/error/empty/data state machine.
//
// Scope: the TABLE is real data; the cheap controls (A–Z filter, per-page,
// Grid/List) work client-side over the loaded list. "Add New Project" opens the
// entry form and a project name opens it in edit mode; the saved-view dropdown is
// still an inert placeholder. The list API returns only contact_id, so we also
// fetch contacts and join by id to show a client name.
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Select from 'primevue/select'
import SelectButton from 'primevue/selectbutton'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { listProjects } from '@/services/projects.service'
import { listContacts } from '@/services/contacts.service'
import { formatMoney } from '@/lib/format'
import type { Project } from '@/types/project'
import type { Contact } from '@/types/contact'
import type { ApiError } from '@/lib/api'

const projects = ref<Project[]>([])
// contact_id → display name, built from the contacts list for the Contact column.
const contactNames = ref<Map<string, string>>(new Map())
const loading = ref(true)
const error = ref('')

const router = useRouter()

// The entry form doubles as the edit form (no separate detail view for projects).
function newProject() {
  router.push('/projects/new')
}
function openEdit(id: string) {
  router.push(`/projects/${id}/edit`)
}

// Saved-view dropdown — rendered but static (no saved-views feature yet).
const savedView = ref('All projects')
const savedViewOptions = ['All projects']

// Grid (the table, default) vs List (stacked cards).
const viewMode = ref<'Grid' | 'List'>('Grid')
const viewModeOptions = ['Grid', 'List']

// Client-side pagination cap.
const perPage = ref(25)
const perPageOptions = [25, 50, 100]

// A–Z filter.
const LETTERS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.split('')
const activeLetter = ref<string>('All')

// A contact's display name: company name, else person name, else email. Same
// precedence as ContactListView — used to build the contact_id → name map.
function contactDisplayName(c: Contact): string {
  const org = c.organisation_name?.trim()
  if (org) return org
  const person = `${c.first_name ?? ''} ${c.last_name ?? ''}`.trim()
  if (person) return person
  return c.email?.trim() || '(no name)'
}

// The client name for a project's contact_id (joined client-side). "—" when the
// contact isn't in the loaded list.
function contactName(p: Project): string {
  return contactNames.value.get(p.contact_id) || '—'
}

// First letter of the project name for the avatar + A–Z bucket.
function firstLetter(p: Project): string {
  return (p.name.trim().charAt(0) || '?').toUpperCase()
}

// Billing rate for display, e.g. "£75.00 / hour" or "£600.00 / day". The API
// sends "0.00" when no rate was entered — show a dash for that.
function billingRate(p: Project): string {
  if (!p.billing_rate || Number(p.billing_rate) === 0) return '—'
  const money = formatMoney(p.billing_rate, p.currency)
  if (p.billing_rate_unit === 'per_hour') return `${money} / hour`
  if (p.billing_rate_unit === 'per_day') return `${money} / day`
  return money
}

// Status pill colours. Projects use lowercase statuses (active / inactive /
// completed / cancelled) — distinct from the expense StatusTag — so the palette
// lives here, reusing the same arbitrary-hex family.
const statusVariants: Record<string, string> = {
  active: 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]',
  inactive: 'bg-[#eef1f4] text-[#5b6772] border-[#dde2e8]',
  completed: 'bg-[#e8f1fb] text-[#1f6fd0] border-[#cfe2f7]',
  cancelled: 'bg-[#fdecec] text-[#c0392b] border-[#f6d3d0]',
}
function statusClass(status: string): string {
  return statusVariants[status] ?? statusVariants.inactive
}
function statusLabel(status: string): string {
  return status.charAt(0).toUpperCase() + status.slice(1)
}

// Which letters actually have projects — highlight live letters, grey the rest.
const availableLetters = computed(() => {
  const set = new Set<string>()
  for (const p of projects.value) {
    const l = firstLetter(p)
    if (l >= 'A' && l <= 'Z') set.add(l)
  }
  return set
})

// projects → filter by letter → sort by name → cap to perPage.
const filtered = computed(() =>
  activeLetter.value === 'All'
    ? projects.value
    : projects.value.filter((p) => firstLetter(p) === activeLetter.value),
)
const sorted = computed(() => [...filtered.value].sort((a, b) => a.name.localeCompare(b.name)))
const paged = computed(() => sorted.value.slice(0, perPage.value))

function selectLetter(l: string) {
  // Only "All" and letters that have projects are selectable.
  if (l === 'All' || availableLetters.value.has(l)) activeLetter.value = l
}

function letterClass(l: string): string {
  if (activeLetter.value === l) return 'bg-fa-blue text-white'
  if (availableLetters.value.has(l)) return 'text-fa-blue hover:bg-[#eef4fb] cursor-pointer'
  return 'text-[#c2c9d1] cursor-default'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    // The list API gives us contact_id only, so fetch contacts alongside and
    // build an id → name map for the Contact column. Both are org-scoped, and a
    // 401 on either is handled by apiFetch (logout + redirect).
    const [projectList, contactList] = await Promise.all([listProjects(), listContacts()])
    projects.value = projectList
    contactNames.value = new Map(contactList.map((c) => [c.id, contactDisplayName(c)]))
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load projects.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Projects</h1>
      <div class="flex gap-2.5">
        <Button label="Add New Project" @click="newProject" />
      </div>
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- Saved view + Grid/List toggle -->
      <div
        class="flex flex-wrap items-center justify-between gap-3 border-b border-fa-border px-4 py-3.5"
      >
        <Select v-model="savedView" :options="savedViewOptions" />
        <SelectButton
          v-model="viewMode"
          :options="viewModeOptions"
          :allow-empty="false"
          aria-label="View mode"
        />
      </div>

      <!-- A–Z bar -->
      <div class="flex flex-wrap items-center gap-1 border-b border-fa-border px-4 py-2.5">
        <button
          type="button"
          class="rounded px-2.5 py-1 text-sm font-semibold"
          :class="activeLetter === 'All' ? 'bg-fa-blue text-white' : 'text-fa-blue hover:bg-[#eef4fb]'"
          @click="selectLetter('All')"
        >
          All
        </button>
        <button
          v-for="l in LETTERS"
          :key="l"
          type="button"
          class="rounded px-2 py-1 text-sm font-semibold"
          :class="letterClass(l)"
          :disabled="!availableLetters.has(l)"
          @click="selectLetter(l)"
        >
          {{ l }}
        </button>
      </div>

      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading projects…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty -->
      <div v-else-if="projects.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No projects yet</p>
        <p class="text-sm text-fa-muted">Projects you add will appear here.</p>
      </div>

      <!-- Data -->
      <template v-else>
        <!-- Grid (table) view -->
        <table v-if="viewMode === 'Grid'" class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Project
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Contact
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Status
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                Billing Rate
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in paged" :key="p.id" class="group hover:bg-[#f7fafc]">
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <div class="flex items-center gap-2.5">
                  <span
                    class="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded bg-[#eef1f4] text-xs font-bold text-fa-muted"
                    >{{ firstLetter(p) }}</span
                  >
                  <button
                    type="button"
                    class="text-left font-semibold text-fa-blue hover:underline"
                    @click="openEdit(p.id)"
                  >
                    {{ p.name }}
                  </button>
                </div>
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ contactName(p) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <span
                  class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
                  :class="statusClass(p.status)"
                  >{{ statusLabel(p.status) }}</span
                >
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums">
                {{ billingRate(p) }}
              </td>
            </tr>
          </tbody>
        </table>

        <!-- List (stacked cards) view -->
        <div v-else class="divide-y divide-[#eef1f4]">
          <div
            v-for="p in paged"
            :key="p.id"
            class="flex items-center justify-between gap-3 px-4 py-3 hover:bg-[#f7fafc]"
          >
            <div class="flex items-center gap-2.5">
              <span
                class="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded bg-[#eef1f4] text-xs font-bold text-fa-muted"
                >{{ firstLetter(p) }}</span
              >
              <div>
                <button
                  type="button"
                  class="block text-left font-semibold text-fa-blue hover:underline"
                  @click="openEdit(p.id)"
                >
                  {{ p.name }}
                </button>
                <div class="text-xs text-fa-muted">{{ contactName(p) }}</div>
              </div>
            </div>
            <div class="flex items-center gap-3">
              <span
                class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
                :class="statusClass(p.status)"
                >{{ statusLabel(p.status) }}</span
              >
              <span class="text-right tabular-nums">{{ billingRate(p) }}</span>
            </div>
          </div>
        </div>

        <!-- Per-page footer -->
        <div
          class="flex items-center gap-2 border-t border-fa-border px-4 py-3 text-sm text-fa-muted"
        >
          <Select v-model="perPage" :options="perPageOptions" />
          <span>per page</span>
        </div>
      </template>
    </div>
  </AppLayout>
</template>
