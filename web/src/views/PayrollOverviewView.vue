<script setup lang="ts">
// Payroll overview — wired to GET /api/v1/payroll/overview. Mirrors the FreeAgent
// Payroll landing page: a Status card + Year-to-date card, a History table of the
// year's pay runs, and an Employees table. Owner/admin only (a 403 surfaces as the
// error state). "Prepare"/"Continue" drives the pay-run wizard.
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { getPayrollOverview, preparePayRun } from '@/services/payroll.service'
import { defaultPaymentDate, monthLabel } from '@/lib/payrollPeriods'
import { formatMoney, formatDate } from '@/lib/format'
import type { Overview } from '@/types/payroll'
import type { ApiError } from '@/lib/api'

const overview = ref<Overview | null>(null)
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const router = useRouter()

const status = computed(() => overview.value?.status)

async function load() {
  loading.value = true
  error.value = ''
  try {
    overview.value = await getPayrollOverview()
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load payroll.'
  } finally {
    loading.value = false
  }
}

function openRun(id: string) {
  router.push(`/payroll/run/${id}`)
}

// Continue the current draft, or prepare the next month then open the wizard.
async function continueOrPrepare() {
  const ov = overview.value
  if (!ov) return
  if (ov.status.state === 'draft' && ov.status.current_run_id) {
    openRun(ov.status.current_run_id)
    return
  }
  if (!ov.status.next_period) return
  busy.value = true
  error.value = ''
  try {
    const run = await preparePayRun({
      tax_year: ov.tax_year,
      period: ov.status.next_period,
      payment_date: defaultPaymentDate(ov.tax_year, ov.status.next_period),
    })
    openRun(run.id)
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not prepare payroll.'
  } finally {
    busy.value = false
  }
}

// The primary button's label depends on whether there's a draft to continue.
const actionLabel = computed(() => {
  const s = status.value
  if (!s) return ''
  if (s.state === 'draft') return `Continue ${monthLabel(s.current_period)} Payroll`
  if (s.can_prepare && s.next_period) return `Prepare ${monthLabel(s.next_period)} Payroll`
  return ''
})

const statusHeadline = computed(() => {
  const s = status.value
  if (!s) return ''
  if (s.state === 'none') return 'No payroll run yet'
  if (s.state === 'draft') return 'Payroll in progress'
  return 'RTI Report Unfiled'
})

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">
        Payroll <span v-if="overview" class="text-fa-muted">{{ overview.tax_year_label }}</span>
      </h1>
      <Button v-if="actionLabel" :label="actionLabel" :loading="busy" @click="continueOrPrepare" />
    </div>

    <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading payroll…
    </div>
    <div v-else-if="error" class="rounded-[5px] border border-fa-border bg-white px-4 py-12 text-center">
      <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
      <Button label="Retry" severity="secondary" outlined @click="load" />
    </div>

    <template v-else-if="overview">
      <!-- Status + Year-to-date cards -->
      <div class="mb-5 grid grid-cols-1 gap-4 lg:grid-cols-3">
        <div class="rounded-[5px] border border-fa-border bg-white p-5 lg:col-span-2">
          <h2 class="mb-3 text-[15px] font-semibold">Status</h2>
          <div class="flex items-start gap-3">
            <i
              class="pi mt-0.5 text-xl"
              :class="status?.state === 'completed_unfiled' ? 'pi-exclamation-circle text-[#e67e22]' : 'pi-info-circle text-fa-blue'"
            />
            <div>
              <p v-if="status && status.current_period > 0" class="text-sm text-fa-muted">
                {{ monthLabel(status.current_period) }}
              </p>
              <p class="text-[17px] font-bold" :class="status?.state === 'completed_unfiled' ? 'text-[#e67e22]' : ''">
                {{ statusHeadline }}
              </p>
              <p v-if="status?.filing_deadline" class="mt-2 text-sm">
                <span class="font-semibold">Filing deadline</span><br />
                {{ formatDate(status.filing_deadline) }}
              </p>
              <p v-if="actionLabel" class="mt-3">
                <button type="button" class="font-semibold text-fa-blue hover:underline" @click="continueOrPrepare">
                  {{ actionLabel }}
                </button>
              </p>
            </div>
          </div>
        </div>

        <div class="rounded-[5px] border border-fa-border bg-white p-5">
          <h2 class="mb-3 text-[15px] font-semibold">Year-to-date</h2>
          <dl class="space-y-2 text-sm">
            <div class="flex justify-between border-b border-[#eef1f4] pb-2">
              <dt class="text-fa-muted">Total Pay</dt>
              <dd class="font-semibold">{{ formatMoney(overview.year_to_date.total_pay) }}</dd>
            </div>
            <div class="flex justify-between border-b border-[#eef1f4] pb-2">
              <dt class="text-fa-muted">Total Tax</dt>
              <dd class="font-semibold">{{ formatMoney(overview.year_to_date.total_tax) }}</dd>
            </div>
            <div class="flex justify-between border-b border-[#eef1f4] pb-2">
              <dt class="text-fa-muted">Total NI</dt>
              <dd class="font-semibold">{{ formatMoney(overview.year_to_date.total_ni) }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-fa-muted">Employment Allowance</dt>
              <dd class="font-semibold">{{ formatMoney(overview.year_to_date.employment_allowance) }}</dd>
            </div>
          </dl>
        </div>
      </div>

      <!-- History -->
      <div class="mb-5 overflow-hidden rounded-[5px] border border-fa-border bg-white">
        <h2 class="border-b border-fa-border px-4 py-3 text-[15px] font-semibold">History</h2>
        <div v-if="!overview.history || overview.history.length === 0" class="px-4 py-10 text-center text-fa-muted">
          No payroll has been run yet.
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Month</th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Date</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Pay</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Tax</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Employee NI</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Employer NI</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Due to HMRC</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="run in overview.history" :key="run.id" class="hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <button type="button" class="font-semibold text-fa-blue hover:underline" @click="openRun(run.id)">
                    {{ monthLabel(run.period) }}
                  </button>
                  <span
                    v-if="run.status === 'draft'"
                    class="ml-2 rounded bg-[#fdebd0] px-1.5 py-0.5 text-[11px] font-semibold text-[#b9770e]"
                    >Draft</span
                  >
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ formatDate(run.payment_date) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.pay) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.tax) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.employee_ni) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.employer_ni) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right font-semibold">{{ formatMoney(run.totals.due_to_hmrc) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Employees -->
      <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
        <h2 class="border-b border-fa-border px-4 py-3 text-[15px] font-semibold">Employees</h2>
        <div v-if="!overview.employees || overview.employees.length === 0" class="px-4 py-10 text-center text-fa-muted">
          No active employees.
        </div>
        <div v-else class="overflow-x-auto">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Name</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Monthly Pay</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Total Pay</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Total Tax</th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Auto-enrolment</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="emp in overview.employees" :key="emp.user_id" class="hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <div class="font-semibold">{{ emp.name }}</div>
                  <div v-if="emp.start_date" class="text-[12px] text-fa-muted">
                    Started {{ formatDate(emp.start_date) }}
                  </div>
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(emp.monthly_pay) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(emp.total_pay) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(emp.total_tax) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">{{ emp.auto_enrolment }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">
                  <Button label="Edit Profile" size="small" severity="secondary" outlined @click="router.push(`/users/${emp.user_id}`)" />
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </AppLayout>
</template>
