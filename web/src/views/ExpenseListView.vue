<script setup lang="ts">
// Expenses list — the "expense view". Wired to GET /api/v1/expenses. The Claimant
// / Status / Range filters are applied CLIENT-SIDE over the loaded rows: the list
// endpoint returns the full set already sorted newest-first (dated_on DESC), so
// there's no need to re-fetch as the filters change. The filtered rows are then
// paginated client-side (page size 25 / 50 / 100).
import { ref, computed, watch, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Select from 'primevue/select'
import MultiSelect from 'primevue/multiselect'
import Button from 'primevue/button'
import Dialog from 'primevue/dialog'
import AppLayout from '@/layouts/AppLayout.vue'
import StatusTag from '@/components/StatusTag.vue'
import { listExpenses, exportExpenses } from '@/services/expenses.service'
import { listMembers } from '@/services/members.service'
import { formatMoney, formatDate, toISODate } from '@/lib/format'
import type { Expense } from '@/types/expense'
import type { OrganisationMember } from '@/types/member'
import type { ApiError } from '@/lib/api'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()

// --- Claimant filter (owner/admin only) ---
// The /members endpoint is admin-only (403 otherwise), and a plain member only
// ever sees their OWN expenses, so the claimant picker is meaningless for them —
// it's hidden in the template and we never call the endpoint. Mirrors the
// claimant picker on the expense form (ExpenseEntryView.vue).
const members = ref<OrganisationMember[]>([])

async function loadMembers() {
  try {
    members.value = await listMembers()
  } catch {
    // A failure here just leaves the claimant list at "All claimants"; the
    // expenses load independently, so we don't surface this as a page error.
    members.value = []
  }
}

// "First Last", falling back to the email when both names are blank. (Same small
// helper as the expense form; kept local to keep this change scoped.)
function personName(p: { first_name?: string; last_name?: string; email: string }): string {
  return `${p.first_name ?? ''} ${p.last_name ?? ''}`.trim() || p.email
}

// 'all' = "All claimants"; otherwise a member's user_id (matched against
// exp.user_id). A non-empty sentinel ('all', not '') is deliberate: PrimeVue's
// Select treats an empty-string value as "no selection" and would show a blank
// box instead of the "All claimants" label.
const selectedClaimant = ref('all')
const claimantOptions = computed(() => [
  { label: 'All claimants', value: 'all' },
  // The endpoint returns members of every status, but you can only meaningfully
  // filter by an active one.
  ...members.value
    .filter((m) => m.status === 'active')
    .map((m) => ({ label: personName(m), value: m.user_id })),
])

// --- Status filter (multi-select) ---
// Values match the raw backend status on exp.status (see StatusTag.vue). PAID is
// omitted — no expense can reach it yet (no transition to PAID; see BACKLOG).
const STATUS_OPTIONS = [
  { label: 'Draft', value: 'DRAFT' },
  { label: 'Submitted', value: 'SUBMITTED' },
  { label: 'Approved', value: 'APPROVED' },
  { label: 'Rejected', value: 'REJECTED' },
]
// [] = all statuses; otherwise only the ticked ones.
const selectedStatuses = ref<string[]>([])

// --- Range filter ---
// Value-backed so the logic doesn't depend on the label text. Default is the 10
// most recent (the list arrives newest-first from the backend).
const RANGE_OPTIONS = [
  { label: '10 most recent', value: 'recent10' },
  { label: 'This month', value: 'month' },
  { label: 'This quarter', value: 'quarter' },
  { label: 'All', value: 'all' },
]
const range = ref('recent10')

const headers = [
  { label: 'Date', num: false },
  { label: 'Description', num: false },
  { label: 'Status', num: false },
  { label: 'Amount', num: true },
]

const expenses = ref<Expense[]>([])
const loading = ref(true)
const error = ref('')

// The list after the Claimant + Status + Range filters. `expenses` arrives sorted
// newest-first, so "10 most recent" is just the first 10 of the filtered rows.
const filteredExpenses = computed(() => {
  const now = new Date()
  const curYear = now.getFullYear()
  const curMonth = now.getMonth() + 1 // 1-12
  const curQuarter = Math.floor(now.getMonth() / 3) // 0-3

  // Parse a "YYYY-MM-DD" date string into its parts directly — avoids the
  // timezone shift you'd get from constructing a Date for a date-only value.
  const partsOf = (datedOn: string) => {
    const [year, month] = datedOn.split('-').map(Number)
    return { year, month }
  }

  let rows = expenses.value

  // 1) Claimant (only an admin ever sets this; non-admins keep 'all').
  if (selectedClaimant.value !== 'all') {
    rows = rows.filter((e) => e.user_id === selectedClaimant.value)
  }

  // 2) Status (empty selection = no status restriction).
  if (selectedStatuses.value.length > 0) {
    rows = rows.filter((e) => selectedStatuses.value.includes(e.status))
  }

  // 3) Range.
  if (range.value === 'month') {
    rows = rows.filter((e) => {
      const { year, month } = partsOf(e.dated_on)
      return year === curYear && month === curMonth
    })
  } else if (range.value === 'quarter') {
    rows = rows.filter((e) => {
      const { year, month } = partsOf(e.dated_on)
      return year === curYear && Math.floor((month - 1) / 3) === curQuarter
    })
  } else if (range.value === 'recent10') {
    rows = rows.slice(0, 10)
  }
  // 'all' → no restriction.

  return rows
})

// --- Pagination ---
// Per the design, the pager only appears once the filtered list runs past 25
// rows; the page size is user-selectable (25 / 50 / 100). It slices the
// already-filtered rows, so it composes with every filter above.
const PER_PAGE_OPTIONS = [
  { label: '25', value: 25 },
  { label: '50', value: 50 },
  { label: '100', value: 100 },
]
const perPage = ref(25)
const currentPage = ref(1) // 1-indexed

const showPagination = computed(() => filteredExpenses.value.length > 25)
const totalPages = computed(() =>
  Math.max(1, Math.ceil(filteredExpenses.value.length / perPage.value)),
)

// The rows to actually render. When the pager is hidden the filtered list is
// <= 25 and perPage is >= 25, so this returns the whole list anyway. currentPage
// is clamped so a transiently out-of-range page never flashes an empty table.
const pagedExpenses = computed(() => {
  const page = Math.min(currentPage.value, totalPages.value)
  const start = (page - 1) * perPage.value
  return filteredExpenses.value.slice(start, start + perPage.value)
})

// Any filter change (filteredExpenses recomputes to a fresh array) or a new page
// size sends you back to page 1, so you can't be stranded on a page that no
// longer exists.
watch([filteredExpenses, perPage], () => {
  currentPage.value = 1
})

function prevPage() {
  if (currentPage.value > 1) currentPage.value--
}
function nextPage() {
  if (currentPage.value < totalPages.value) currentPage.value++
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    expenses.value = await listExpenses()
  } catch (err) {
    // A 401 is already handled by apiFetch (logout + redirect); any other
    // failure shows here with a retry.
    error.value = (err as ApiError)?.message ?? 'Could not load expenses.'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  // Only owner/admins may list members — and only they get the claimant filter.
  if (auth.isOrgAdmin) loadMembers()
})

function openExpense(id: string) {
  router.push(`/expenses/${id}`)
}

function newExpense() {
  router.push('/expenses/new')
}

// --- Export ---
// Download the on-screen expenses as a CSV — the rows left after the Claimant /
// Status / Range filters (across all pages, not just the current one). We send
// their ids so the CSV matches the list exactly; to export everything, clear the
// filters (Range = All). The endpoint is authenticated, so rather than a plain
// <a href> we fetch the Blob (token attached by apiDownload) and click a throwaway
// object-URL anchor. The backend still enforces who may see each row.
const exporting = ref(false)
const exportError = ref('')

async function exportCsv() {
  exporting.value = true
  exportError.value = ''
  try {
    const ids = filteredExpenses.value.map((e) => e.id)
    const blob = await exportExpenses(ids)
    triggerDownload(blob, `expenses-${toISODate(new Date())}.csv`)
  } catch (err) {
    exportError.value = (err as ApiError)?.message ?? 'Could not export expenses.'
  } finally {
    exporting.value = false
  }
}

// Save a Blob to disk by clicking a throwaway object-URL anchor, then revoking it.
function triggerDownload(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// --- Import (dialog) ---
// The import button opens a dialog that ships the CSV template + a field guide.
// Parsing an uploaded file into expenses is a planned follow-up; for now the
// dialog's job is to hand over a correctly-shaped template. The template is a
// static file in /public, served at the web root (BASE_URL handles a sub-path).
const showImport = ref(false)
const templateHref = `${import.meta.env.BASE_URL}expense_import_template.csv`

// Mirrors the export's columns and the shipped template — the single source of
// truth for "how to fill in our expenses template".
const templateFields = [
  {
    name: 'claimant_email',
    required: false,
    format:
      'Email of the person the expense is for (an active member of your organisation). Blank → you.',
  },
  {
    name: 'category',
    required: true,
    format:
      'Category name (e.g. Travel) or nominal code (e.g. 6042). Must match one of your categories.',
  },
  { name: 'date', required: true, format: 'Date on the receipt, DD/MM/YYYY (e.g. 17/06/2026).' },
  { name: 'currency', required: false, format: '3-letter ISO code (GBP, USD). Defaults to GBP.' },
  {
    name: 'gross_value',
    required: true,
    format: 'Total incl. VAT as a decimal (e.g. 42.50). No symbol or thousands separators.',
  },
  { name: 'description', required: true, format: 'Short description of the expense.' },
  { name: 'supplier_name', required: false, format: 'Who you paid (e.g. Trainline).' },
  { name: 'receipt_reference', required: false, format: 'Your own reference for the receipt.' },
  { name: 'invoice_number', required: false, format: 'The supplier’s invoice / receipt number.' },
  {
    name: 'sales_tax_rate',
    required: false,
    format: 'VAT rate as a percentage number (e.g. 20, 0). Blank → no VAT.',
  },
  {
    name: 'sales_tax_value',
    required: false,
    format:
      'VAT amount in the same currency (e.g. 7.08). Calculated for standard rates if left blank.',
  },
  {
    name: 'ec_status',
    required: false,
    format: 'UK_NON_EC (default), EC_GOODS, EC_SERVICES, or REVERSE_CHARGE.',
  },
]
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Out-of-Pocket Expenses</h1>
      <div class="flex flex-wrap gap-2.5">
        <Button label="Import expenses" severity="secondary" outlined @click="showImport = true" />
        <Button
          label="Export expenses"
          severity="secondary"
          outlined
          :loading="exporting"
          :disabled="filteredExpenses.length === 0"
          @click="exportCsv"
        />
        <Button label="Add new" icon="pi pi-angle-down" icon-pos="right" @click="newExpense" />
      </div>
    </div>

    <!-- Export failures surface here (a list-load error has its own state below). -->
    <p v-if="exportError" class="mb-3 text-sm text-[#c0392b]">{{ exportError }}</p>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <div class="flex flex-wrap gap-3 border-b border-fa-border px-4 py-3.5">
        <!-- Claimant: owner/admin only (a plain member only ever sees their own expenses). -->
        <Select
          v-if="auth.isOrgAdmin"
          v-model="selectedClaimant"
          :options="claimantOptions"
          option-label="label"
          option-value="value"
        />
        <MultiSelect
          v-model="selectedStatuses"
          :options="STATUS_OPTIONS"
          option-label="label"
          option-value="value"
          placeholder="All statuses"
          display="chip"
        />
        <Select
          v-model="range"
          :options="RANGE_OPTIONS"
          option-label="label"
          option-value="value"
        />
      </div>

      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading expenses…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty: no expenses at all -->
      <div v-else-if="expenses.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No expenses yet</p>
        <p class="mb-4 text-sm text-fa-muted">Add your first out-of-pocket expense to see it here.</p>
        <Button label="New expense" @click="newExpense" />
      </div>

      <!-- Empty: there are expenses, but none match the current filters -->
      <div v-else-if="filteredExpenses.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No expenses match these filters</p>
        <p class="text-sm text-fa-muted">Try clearing the claimant, status, or date range.</p>
      </div>

      <!-- Data -->
      <div v-else>
        <div class="overflow-x-auto">
        <table class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th
                v-for="h in headers"
                :key="h.label"
                class="border-b border-fa-border px-4 py-3 text-[13px] font-semibold text-fa-muted"
                :class="h.num ? 'text-right' : 'text-left'"
              >
                {{ h.label }}
              </th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="exp in pagedExpenses"
              :key="exp.id"
              class="cursor-pointer hover:bg-[#f7fafc]"
              @click="openExpense(exp.id)"
            >
              <td class="whitespace-nowrap border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                {{ formatDate(exp.dated_on) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <RouterLink
                  :to="{ name: 'expense-detail', params: { id: exp.id } }"
                  class="font-semibold text-fa-blue hover:underline"
                  @click.stop
                  >{{ exp.description }}</RouterLink
                >
                <span v-if="exp.supplier_name" class="ml-2 text-fa-muted">{{ exp.supplier_name }}</span>
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <StatusTag :status="exp.status" />
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums">
                {{ formatMoney(exp.gross_value, exp.currency) }}
              </td>
            </tr>
          </tbody>
        </table>
        </div>

        <!-- Pagination — only for lists longer than 25 rows; sits bottom-left. -->
        <div
          v-if="showPagination"
          class="flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-fa-border px-4 py-3 text-sm"
        >
          <div class="flex items-center gap-2">
            <Select
              v-model="perPage"
              :options="PER_PAGE_OPTIONS"
              option-label="label"
              option-value="value"
            />
            <span class="text-fa-muted">per page</span>
          </div>
          <div class="flex items-center gap-2">
            <Button
              label="Previous"
              icon="pi pi-angle-left"
              severity="secondary"
              outlined
              size="small"
              :disabled="currentPage === 1"
              @click="prevPage"
            />
            <span class="text-fa-muted">Page {{ currentPage }} of {{ totalPages }}</span>
            <Button
              label="Next"
              icon="pi pi-angle-right"
              icon-pos="right"
              severity="secondary"
              outlined
              size="small"
              :disabled="currentPage === totalPages"
              @click="nextPage"
            />
          </div>
        </div>
      </div>
    </div>

    <!-- Import expenses: download the template + how to fill it in. Parsing an
         uploaded file into expenses is a planned follow-up; this dialog ships the
         template now. -->
    <Dialog v-model:visible="showImport" modal header="Import expenses" :style="{ width: '46rem' }">
      <p class="mb-4 text-sm text-fa-muted">
        Bulk-add expenses from a spreadsheet. Download the template, fill in one row per expense
        (keep the column headers unchanged), and save as CSV. Uploading a completed file will be
        enabled soon.
      </p>

      <a
        :href="templateHref"
        download="expense_import_template.csv"
        class="mb-5 inline-flex items-center gap-2 rounded-[5px] border border-fa-border bg-white px-3.5 py-2 text-sm font-semibold text-fa-blue hover:bg-[#f7fafc]"
      >
        <i class="pi pi-download" />Download template
      </a>

      <h3 class="mb-2 text-sm font-bold">How to fill in our expenses template</h3>
      <div class="overflow-hidden rounded-[5px] border border-fa-border">
        <table class="w-full border-collapse text-[13px]">
          <thead>
            <tr class="bg-[#f7fafc] text-left text-fa-muted">
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Column</th>
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Required</th>
              <th class="border-b border-fa-border px-3 py-2 font-semibold">Format / values</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="f in templateFields" :key="f.name" class="align-top">
              <td class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2 font-mono">
                {{ f.name }}
              </td>
              <td class="whitespace-nowrap border-b border-[#eef1f4] px-3 py-2">
                {{ f.required ? 'Required' : 'Optional' }}
              </td>
              <td class="border-b border-[#eef1f4] px-3 py-2">{{ f.format }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <Button label="Close" severity="secondary" outlined @click="showImport = false" />
      </template>
    </Dialog>
  </AppLayout>
</template>
