<script setup lang="ts">
// Pay-run wizard — GET /api/v1/payroll/periods/:id. Mirrors the FreeAgent "Prepare
// Month N Payroll" screen: review the payslips, then "Run & Report" (complete) or
// "Delete". A draft run is editable (per-payslip Edit/View); a completed run is
// read-only. Owner/admin only.
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { getPayRun, completePayRun, deletePayRun, refreshPayRun } from '@/services/payroll.service'
import { monthLabel } from '@/lib/payrollPeriods'
import { formatMoney, formatDate } from '@/lib/format'
import type { PayRun } from '@/types/payroll'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const run = ref<PayRun | null>(null)
const loading = ref(true)
const error = ref('')
const busy = ref(false)

const id = computed(() => route.params.id as string)
const isDraft = computed(() => run.value?.status === 'draft')
const payslips = computed(() => run.value?.payslips ?? [])

async function load() {
  loading.value = true
  error.value = ''
  try {
    run.value = await getPayRun(id.value)
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load the pay run.'
  } finally {
    loading.value = false
  }
}

async function runReport() {
  if (!run.value) return
  busy.value = true
  error.value = ''
  try {
    run.value = await completePayRun(run.value.id)
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not complete the pay run.'
  } finally {
    busy.value = false
  }
}

async function refreshFromProfiles() {
  if (!run.value) return
  if (!confirm('Re-pull every payslip from the current employee profiles? This discards manual edits made to this run’s payslips.')) return
  busy.value = true
  error.value = ''
  try {
    run.value = await refreshPayRun(run.value.id)
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not refresh the pay run.'
  } finally {
    busy.value = false
  }
}

async function removeRun() {
  if (!run.value) return
  if (!confirm(`Delete ${monthLabel(run.value.period)} payroll? This cannot be undone.`)) return
  busy.value = true
  error.value = ''
  try {
    await deletePayRun(run.value.id)
    router.push('/payroll')
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not delete the pay run.'
    busy.value = false
  }
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading…
    </div>
    <div v-else-if="error && !run" class="rounded-[5px] border border-fa-border bg-white px-4 py-12 text-center">
      <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
      <Button label="Back to payroll" severity="secondary" outlined @click="router.push('/payroll')" />
    </div>

    <template v-else-if="run">
      <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-[22px] font-bold">
          {{ isDraft ? 'Prepare' : '' }} {{ monthLabel(run.period) }} Payroll
          <span class="text-fa-muted">to {{ formatDate(run.period_end) }}</span>
        </h1>
        <div class="flex gap-2">
          <Button label="Back" severity="secondary" outlined @click="router.push('/payroll')" />
          <template v-if="isDraft">
            <Button label="Refresh from profiles" severity="secondary" outlined :disabled="busy" @click="refreshFromProfiles" />
            <Button label="Delete Payroll" severity="danger" outlined :disabled="busy" @click="removeRun" />
            <Button label="Run &amp; Report" :loading="busy" @click="runReport" />
          </template>
        </div>
      </div>

      <p v-if="error" class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>

      <div class="mb-4 flex flex-wrap gap-6 rounded-[5px] border border-fa-border bg-white px-4 py-3 text-sm">
        <div><span class="text-fa-muted">Payment date:</span> <span class="font-semibold">{{ formatDate(run.payment_date) }}</span></div>
        <div>
          <span class="text-fa-muted">Status:</span>
          <span class="font-semibold">{{ isDraft ? 'Draft (report unfiled)' : 'Completed (RTI unfiled)' }}</span>
        </div>
        <div><span class="text-fa-muted">Employment Allowance:</span> <span class="font-semibold">{{ formatMoney(run.employment_allowance_amount) }}</span></div>
      </div>

      <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
        <div class="overflow-x-auto">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Name</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Total Pay</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Tax</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Employee NI</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Net Pay</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Employer NI</th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="ps in payslips" :key="ps.id" class="hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle font-semibold">{{ ps.employee_name }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(ps.gross_pay) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(ps.tax_deducted) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(ps.employee_ni) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right font-semibold">{{ formatMoney(ps.net_pay) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(ps.employer_ni) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">
                  <div class="flex justify-end gap-2">
                    <Button label="View" size="small" severity="secondary" text @click="router.push(`/payroll/payslips/${ps.id}`)" />
                    <Button
                      v-if="isDraft"
                      label="Edit Payslip"
                      size="small"
                      severity="secondary"
                      outlined
                      @click="router.push(`/payroll/payslips/${ps.id}/edit`)"
                    />
                  </div>
                </td>
              </tr>
            </tbody>
            <tfoot>
              <tr class="bg-[#f7fafc] font-semibold">
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">Totals</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.pay) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.tax) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.employee_ni) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.net_pay) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-right">{{ formatMoney(run.totals.employer_ni) }}</td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle"></td>
              </tr>
            </tfoot>
          </table>
        </div>
      </div>
    </template>
  </AppLayout>
</template>
