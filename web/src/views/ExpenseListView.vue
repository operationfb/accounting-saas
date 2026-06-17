<script setup lang="ts">
// Expenses list — the "expense view". Wired to GET /api/v1/expenses. The Claimant
// / Status / Range filters are applied CLIENT-SIDE over the loaded rows: the list
// endpoint returns the full set already sorted newest-first (dated_on DESC), so
// there's no need to re-fetch as the filters change.
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Select from 'primevue/select'
import MultiSelect from 'primevue/multiselect'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import StatusTag from '@/components/StatusTag.vue'
import { listExpenses } from '@/services/expenses.service'
import { listMembers } from '@/services/members.service'
import { formatMoney, formatDate } from '@/lib/format'
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
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Out-of-Pocket Expenses</h1>
      <div class="flex gap-2.5">
        <Button label="Import expenses" severity="secondary" outlined />
        <Button label="Add new" icon="pi pi-angle-down" icon-pos="right" @click="newExpense" />
      </div>
    </div>

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
      <table v-else class="w-full border-collapse text-sm">
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
            v-for="exp in filteredExpenses"
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
  </AppLayout>
</template>
