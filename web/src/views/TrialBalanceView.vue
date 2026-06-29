<script setup lang="ts">
// Trial Balance report — wired to GET /api/v1/reports/trial-balance. Mirrors the
// list views (AppLayout wrapper, a hand-rolled Tailwind table with the fa-* theme
// colours, and a loading/error/empty/data state machine).
//
// Iteration 1 is a TODAY snapshot: every Chart-of-Accounts account that has at
// least one journal line, with its balance in the Debit or Credit column, and a
// balancing "Trial Balance Check" foot row. The Date control is a single "Today"
// option for now (the ?date param is plumbed in the service for later date ranges).
import { ref, onMounted } from 'vue'
import Select from 'primevue/select'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { getTrialBalance } from '@/services/reports.service'
import { formatNumber, formatDate } from '@/lib/format'
import type { TrialBalance } from '@/types/report'
import type { ApiError } from '@/lib/api'

const tb = ref<TrialBalance | null>(null)
const loading = ref(true)
const error = ref('')

// Date control: only "Today" for iteration 1 (decorative — the report is always
// as-of-today). Kept as a Select so adding date options later is a one-liner.
const dateOption = ref('today')
const dateOptions = [{ label: 'Today', value: 'today' }]

// The check totals are shown to the nearest £ (matching the mockup's "nearest £").
function toNearestPound(pounds: string): string {
  const n = Number(pounds)
  if (Number.isNaN(n)) return pounds
  return `£${formatNumber(String(Math.round(n)), 0)}`
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    tb.value = await getTrialBalance()
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load the trial balance.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <!-- Date control card (FreeAgent-style filter strip) -->
    <div class="mb-4 rounded-[5px] border border-fa-border bg-white px-4 py-3">
      <label class="mb-1 block text-[13px] font-semibold text-fa-muted">Date</label>
      <Select
        v-model="dateOption"
        :options="dateOptions"
        option-label="label"
        option-value="value"
        class="w-48"
      />
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading trial balance…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Data (header + table; empty ledger renders an empty table with zeroed check) -->
      <template v-else-if="tb">
        <div class="px-6 pt-6">
          <h1 class="text-[22px] font-bold">Trial Balance</h1>
          <p class="mt-0.5 text-sm text-fa-muted">As of {{ formatDate(tb.as_of_date) }}</p>
        </div>

        <!-- Empty ledger -->
        <div v-if="!tb.rows || tb.rows.length === 0" class="px-6 py-14 text-center">
          <p class="mb-1 font-semibold">No transactions yet</p>
          <p class="text-sm text-fa-muted">
            Accounts with posted journal entries will appear here.
          </p>
        </div>

        <div v-else class="overflow-x-auto px-2 pb-2">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Account
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
              <tr v-for="row in tb.rows" :key="row.nominal_code" class="hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-2.5 align-middle">
                  <!-- Code + name link to the Account Transactions drill-down for
                       this account (the per-account ledger). -->
                  <RouterLink
                    :to="`/reports/account-transactions?account=${encodeURIComponent(row.nominal_code)}`"
                    class="text-fa-blue hover:underline"
                  >
                    <span class="font-semibold">{{ row.nominal_code }}</span>
                    <span class="ml-1.5">{{ row.name }}</span>
                  </RouterLink>
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
                <td class="px-4 py-3 align-middle">Trial Balance Check (nearest £)</td>
                <td class="px-4 py-3 text-right align-middle tabular-nums">
                  {{ toNearestPound(tb.total_debit) }}
                </td>
                <td class="px-4 py-3 text-right align-middle tabular-nums">
                  {{ toNearestPound(tb.total_credit) }}
                </td>
              </tr>
            </tfoot>
          </table>
        </div>
      </template>
    </div>
  </AppLayout>
</template>
