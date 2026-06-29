<script setup lang="ts">
// Account Transactions report — the per-account drill-down reached from the Trial
// Balance (click an account code) or from the Reports menu. Wired to
// GET /api/v1/reports/account-transactions (+ /reports/accounts for the picker).
// Mirrors TrialBalanceView's structure (AppLayout, fa-* themed table, loading/
// error/empty/data state machine) plus a filter strip (Date range + Account).
//
// Iteration 1: one account at a time, over a date range. Default range is "All
// time" so a drill-down from the cumulative Trial Balance reconciles with the
// account's balance. No "Has attachments" filter (dropped for now).
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import Select from 'primevue/select'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { listReportAccounts, getAccountTransactions } from '@/services/reports.service'
import { formatNumber, formatDate } from '@/lib/format'
import type { AccountSummary, AccountTransactions } from '@/types/report'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()

const accounts = ref<AccountSummary[]>([])
const selectedAccount = ref<string>('') // the nominal code
const datePreset = ref<string>('all')

const report = ref<AccountTransactions | null>(null)
const loading = ref(false)
const error = ref('')

// Date-range presets, computed client-side to concrete { from, to } bounds. "All"
// returns no lower bound (open). "Year to date" uses the CALENDAR year (a documented
// stand-in — the org has no stored accounting-year start yet).
const datePresets = [
  { label: 'All time', value: 'all' },
  { label: 'This month', value: 'month' },
  { label: 'This quarter', value: 'quarter' },
  { label: 'Year to date', value: 'ytd' },
  { label: 'Last 12 months', value: '12months' },
]

function pad(n: number): string {
  return String(n).padStart(2, '0')
}
function iso(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}
// Returns { from?, to } for the active preset; `from` undefined = open lower bound.
function rangeFor(preset: string): { from?: string; to: string } {
  const now = new Date()
  const to = iso(now)
  const y = now.getFullYear()
  switch (preset) {
    case 'month':
      return { from: `${y}-${pad(now.getMonth() + 1)}-01`, to }
    case 'quarter': {
      const qStartMonth = Math.floor(now.getMonth() / 3) * 3 // 0,3,6,9
      return { from: `${y}-${pad(qStartMonth + 1)}-01`, to }
    }
    case 'ytd':
      return { from: `${y}-01-01`, to }
    case '12months': {
      const start = new Date(now)
      start.setFullYear(start.getFullYear() - 1)
      return { from: iso(start), to }
    }
    default: // 'all'
      return { to }
  }
}

// Account picker grouped by CoA section, like BillEntryView's category select.
const GROUP_LABELS: Record<string, string> = {
  INCOME: 'Income',
  OTHER_INCOME: 'Other Income',
  COST_OF_SALES: 'Cost of Sales',
  ADMIN_EXPENSE: 'Admin Expenses',
  PAYROLL_EXPENSE: 'Payroll',
  CAPITAL_ASSET: 'Capital Assets',
  CURRENT_ASSET: 'Current Assets',
  BANK: 'Bank',
  LIABILITY: 'Liabilities',
  TAX_LIABILITY: 'Tax Liabilities',
  USER_ACCOUNT: 'User Accounts',
  EQUITY: 'Equity',
  SYSTEM: 'System',
}
const accountGroups = computed(() => {
  const groups = new Map<string, { label: string; value: string }[]>()
  for (const a of accounts.value) {
    const group = GROUP_LABELS[a.account_type] ?? a.account_type
    if (!groups.has(group)) groups.set(group, [])
    groups.get(group)!.push({ label: `${a.nominal_code} ${a.name}`, value: a.nominal_code })
  }
  return [...groups.entries()].map(([group, items]) => ({ group, items }))
})

// The report subtitle: the resolved date range (honest wording — no "accounting
// year" claim). An open lower bound reads as "Up to <to>".
const rangeLabel = computed(() => {
  if (!report.value) return ''
  const to = formatDate(report.value.to_date)
  return report.value.from_date ? `${formatDate(report.value.from_date)} – ${to}` : `Up to ${to}`
})

// Source-document link for the Description cell. Only the well-understood source
// types link in v1; the rest (bank/payroll/manual/etc.) render as plain text.
function sourceLink(sourceType: string, sourceId: string): string | null {
  if (!sourceId) return null
  switch (sourceType) {
    case 'INVOICE':
    case 'INVOICE_RECEIPT':
      return `/invoices/${sourceId}`
    case 'EXPENSE':
      return `/expenses/${sourceId}`
    case 'BILL':
    case 'BILL_PAYMENT':
      return `/bills/${sourceId}/edit`
    default:
      return null
  }
}

async function loadAccounts() {
  try {
    accounts.value = await listReportAccounts()
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load the account list.'
  }
}

async function load() {
  if (!selectedAccount.value) return
  loading.value = true
  error.value = ''
  try {
    const { from, to } = rangeFor(datePreset.value)
    report.value = await getAccountTransactions(selectedAccount.value, from, to)
  } catch (err) {
    report.value = null
    error.value = (err as ApiError)?.message ?? 'Could not load the account transactions.'
  } finally {
    loading.value = false
  }
}

// Apply keeps the chosen account in the URL (so the view is linkable/refreshable)
// and reloads. The watch on route.query.account below triggers the actual load.
function apply() {
  if (!selectedAccount.value) return
  if (route.query.account === selectedAccount.value) {
    load() // same account, only the date changed → reload directly
  } else {
    router.replace({ query: { ...route.query, account: selectedAccount.value } })
  }
}

onMounted(loadAccounts)

// Pre-select + load from the ?account= query param (the Trial Balance links here as
// /reports/account-transactions?account=001). immediate so a direct visit works too.
watch(
  () => route.query.account,
  (raw) => {
    if (typeof raw === 'string' && raw) {
      selectedAccount.value = raw
      load()
    }
  },
  { immediate: true },
)
</script>

<template>
  <AppLayout>
    <!-- Filter strip (FreeAgent-style) -->
    <div class="mb-4 flex flex-wrap items-end gap-4 rounded-[5px] border border-fa-border bg-white px-4 py-3">
      <div>
        <label class="mb-1 block text-[13px] font-semibold text-fa-muted">Date range</label>
        <Select
          v-model="datePreset"
          :options="datePresets"
          option-label="label"
          option-value="value"
          class="w-52"
        />
      </div>
      <div>
        <label class="mb-1 block text-[13px] font-semibold text-fa-muted">Account</label>
        <Select
          v-model="selectedAccount"
          :options="accountGroups"
          option-label="label"
          option-value="value"
          option-group-label="group"
          option-group-children="items"
          placeholder="Choose an account…"
          filter
          class="w-80"
        />
      </div>
      <Button label="Apply" :disabled="!selectedAccount" @click="apply" />
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- No account chosen yet -->
      <div v-if="!selectedAccount" class="px-6 py-14 text-center">
        <p class="mb-1 font-semibold">Choose an account</p>
        <p class="text-sm text-fa-muted">
          Pick an account above to see its transactions, or click an account on the Trial Balance.
        </p>
      </div>

      <!-- Loading -->
      <div v-else-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading transactions…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Data -->
      <template v-else-if="report">
        <div class="px-6 pt-6">
          <h1 class="text-[22px] font-bold">Account Transactions</h1>
          <p class="mt-0.5 text-sm text-fa-muted">{{ rangeLabel }}</p>
        </div>

        <div class="px-6 pt-5">
          <h2 class="text-[17px] font-bold">{{ report.nominal_code }} {{ report.name }}</h2>
        </div>

        <!-- Empty (account has no lines in range) -->
        <div v-if="!report.rows || report.rows.length === 0" class="px-6 py-12 text-center">
          <p class="text-sm text-fa-muted">No transactions for this account in the selected period.</p>
        </div>

        <div v-else class="overflow-x-auto px-2 pb-2 pt-2">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Date
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Description
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                  Debit
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                  Credit
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(row, i) in report.rows" :key="i" class="hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-2.5 align-middle text-fa-muted whitespace-nowrap">
                  {{ formatDate(row.date) }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-2.5 align-middle">
                  <RouterLink
                    v-if="sourceLink(row.source_type, row.source_id)"
                    :to="sourceLink(row.source_type, row.source_id)!"
                    class="text-fa-blue hover:underline"
                  >
                    {{ row.description }}
                  </RouterLink>
                  <span v-else>{{ row.description }}</span>
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-2.5 text-right align-middle tabular-nums">
                  {{ row.debit ? formatNumber(row.debit) : '' }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-2.5 text-right align-middle tabular-nums">
                  {{ row.credit ? formatNumber(row.credit) : '' }}
                </td>
              </tr>
            </tbody>
            <tfoot>
              <tr class="font-semibold">
                <td class="px-4 py-3 align-middle" colspan="2">Total</td>
                <td class="px-4 py-3 text-right align-middle tabular-nums">
                  {{ report.total_debit !== '0.00' ? formatNumber(report.total_debit) : '' }}
                </td>
                <td class="px-4 py-3 text-right align-middle tabular-nums">
                  {{ report.total_credit !== '0.00' ? formatNumber(report.total_credit) : '' }}
                </td>
              </tr>
            </tfoot>
          </table>
        </div>
      </template>
    </div>
  </AppLayout>
</template>
