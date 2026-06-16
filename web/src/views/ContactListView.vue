<script setup lang="ts">
// Contacts list — the "contacts view". Wired to GET /api/v1/contacts.
// Mirrors ExpenseListView: AppLayout wrapper, PrimeVue Select/Button, a
// hand-rolled Tailwind table with the fa-* theme colours, and a
// loading/error/empty/data state machine.
//
// Scope (per the approved plan): the TABLE is real data; the cheap controls
// (A–Z filter, per-page, Grid/List) work client-side over the loaded list.
// Import/Export, the saved-view dropdown, the per-row Edit/Add-new, and the
// Active Projects / Account Balance columns are faithful but INERT placeholders —
// the backend has no projects, balances, saved views, or import/export yet.
import { ref, computed, onMounted } from 'vue'
import Select from 'primevue/select'
import SelectButton from 'primevue/selectbutton'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { listContacts } from '@/services/contacts.service'
import { formatMoney } from '@/lib/format'
import type { Contact } from '@/types/contact'
import type { ApiError } from '@/lib/api'

const contacts = ref<Contact[]>([])
const loading = ref(true)
const error = ref('')

// Saved-view dropdown — rendered but static (no saved-views feature yet).
const savedView = ref('All contacts')
const savedViewOptions = ['All contacts']

// Grid (the table, default — matches the screenshot) vs List (stacked cards).
const viewMode = ref<'Grid' | 'List'>('Grid')
const viewModeOptions = ['Grid', 'List']

// Client-side pagination cap.
const perPage = ref(25)
const perPageOptions = [25, 50, 100]

// A–Z filter.
const LETTERS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.split('')
const activeLetter = ref<string>('All')

// Account Balance is a placeholder until there's a ledger — every row and the
// total are £0.00.
const ZERO = formatMoney('0.00', 'GBP')

// A contact's display name: company name, else person name, else email.
function displayName(c: Contact): string {
  const org = c.organisation_name?.trim()
  if (org) return org
  const person = `${c.first_name ?? ''} ${c.last_name ?? ''}`.trim()
  if (person) return person
  return c.email?.trim() || '(no name)'
}

// Secondary line / Details column — first of email, phone, town.
function details(c: Contact): string {
  return c.email?.trim() || c.telephone?.trim() || c.town?.trim() || ''
}

// First letter for the avatar + A–Z bucket.
function firstLetter(c: Contact): string {
  return (displayName(c).charAt(0) || '?').toUpperCase()
}

// Which letters actually have contacts — used to highlight live letters and grey
// out the empty ones (the screenshot colours "T" blue, the rest grey).
const availableLetters = computed(() => {
  const set = new Set<string>()
  for (const c of contacts.value) {
    const l = firstLetter(c)
    if (l >= 'A' && l <= 'Z') set.add(l)
  }
  return set
})

// contacts → filter by letter → sort by name → cap to perPage.
const filtered = computed(() =>
  activeLetter.value === 'All'
    ? contacts.value
    : contacts.value.filter((c) => firstLetter(c) === activeLetter.value),
)
const sorted = computed(() =>
  [...filtered.value].sort((a, b) => displayName(a).localeCompare(displayName(b))),
)
const paged = computed(() => sorted.value.slice(0, perPage.value))

function selectLetter(l: string) {
  // Only "All" and letters that have contacts are selectable.
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
    contacts.value = await listContacts()
  } catch (err) {
    // A 401 is already handled by apiFetch (logout + redirect); anything else
    // shows here with a retry.
    error.value = (err as ApiError)?.message ?? 'Could not load contacts.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Contacts</h1>
      <div class="flex gap-2.5">
        <Button label="Import Contacts" severity="secondary" outlined />
        <Button label="Export Contacts" severity="secondary" outlined />
        <Button label="Add New Contact" />
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
        <i class="pi pi-spin pi-spinner mr-2" />Loading contacts…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty -->
      <div v-else-if="contacts.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No contacts yet</p>
        <p class="mb-4 text-sm text-fa-muted">Add your first contact to see it here.</p>
        <Button label="Add New Contact" />
      </div>

      <!-- Data -->
      <template v-else>
        <!-- Grid (table) view -->
        <table v-if="viewMode === 'Grid'" class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Contact
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Details
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Active Projects
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                Account Balance
              </th>
              <th class="border-b border-fa-border px-4 py-3" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in paged" :key="c.id" class="group hover:bg-[#f7fafc]">
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <div class="flex items-center gap-2.5">
                  <span
                    class="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded bg-[#eef1f4] text-xs font-bold text-fa-muted"
                    >{{ firstLetter(c) }}</span
                  >
                  <span class="font-semibold text-fa-blue">{{ displayName(c) }}</span>
                </div>
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ details(c) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">—</td>
              <td
                class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums"
              >
                {{ ZERO }}
              </td>
              <td class="whitespace-nowrap border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle">
                <!-- Inert per-row actions (no entry form yet); shown on row hover. -->
                <span class="invisible inline-flex gap-2 group-hover:visible">
                  <Button label="Edit" size="small" severity="secondary" outlined />
                  <Button
                    label="Add new"
                    icon="pi pi-angle-down"
                    icon-pos="right"
                    size="small"
                    severity="secondary"
                    outlined
                  />
                </span>
              </td>
            </tr>
          </tbody>
          <tfoot>
            <tr>
              <td colspan="3" class="px-4 py-3" />
              <td class="px-4 py-3 text-right align-middle tabular-nums">
                <span class="text-fa-muted">Total</span>
                <span class="ml-2 font-semibold">{{ ZERO }}</span>
              </td>
              <td class="px-4 py-3" />
            </tr>
          </tfoot>
        </table>

        <!-- List (stacked cards) view -->
        <div v-else class="divide-y divide-[#eef1f4]">
          <div
            v-for="c in paged"
            :key="c.id"
            class="flex items-center justify-between gap-3 px-4 py-3 hover:bg-[#f7fafc]"
          >
            <div class="flex items-center gap-2.5">
              <span
                class="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded bg-[#eef1f4] text-xs font-bold text-fa-muted"
                >{{ firstLetter(c) }}</span
              >
              <div>
                <div class="font-semibold text-fa-blue">{{ displayName(c) }}</div>
                <div v-if="details(c)" class="text-xs text-fa-muted">{{ details(c) }}</div>
              </div>
            </div>
            <div class="text-right tabular-nums">{{ ZERO }}</div>
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
