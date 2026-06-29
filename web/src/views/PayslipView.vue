<script setup lang="ts">
// Payslip document — GET /api/v1/payroll/payslips/:id. The read-only Payments /
// Deductions / Year-to-date layout (FreeAgent's payslip). Owner/admin only.
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { getPayslip } from '@/services/payroll.service'
import { formatMoney } from '@/lib/format'
import type { Payslip } from '@/types/payroll'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const ps = ref<Payslip | null>(null)
const loading = ref(true)
const error = ref('')

const id = computed(() => route.params.id as string)

// Payment lines shown only when non-zero (keeps the slip uncluttered).
const payments = computed(() => {
  if (!ps.value) return []
  const p = ps.value
  return [
    ['Basic Pay', p.basic_pay],
    ['Overtime', p.overtime],
    ['Bonus', p.bonus],
    ['Commission', p.commission],
    ['Allowance', p.allowance],
    ['Holiday Pay', p.holiday_pay],
    ['Absence Payments', p.absence_payments],
    ['Statutory Sick Pay', p.statutory_sick_pay],
    ['Statutory Maternity Pay', p.statutory_maternity_pay],
    ['Statutory Paternity Pay', p.statutory_paternity_pay],
    ['Other Payments', p.other_payments],
    ['Pay Not Subject to Tax/NI', p.pay_not_subject_to_tax_ni],
  ].filter(([, v]) => Number(v) !== 0) as [string, string][]
})

const deductions = computed(() => {
  if (!ps.value) return []
  const p = ps.value
  return [
    ['PAYE Tax', p.tax_deducted],
    ['Employee NI', p.employee_ni],
    ['Employee Pension', p.employee_pension],
    ['Student Loan', p.student_loan],
    ['Payroll Giving', p.payroll_giving],
    ['Other Deductions', p.other_deductions_net_pay],
    ['Salary Sacrifice', p.salary_sacrifice_deductions],
  ].filter(([, v]) => Number(v) !== 0) as [string, string][]
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    ps.value = await getPayslip(id.value)
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load the payslip.'
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading…
    </div>
    <div v-else-if="error" class="rounded-[5px] border border-fa-border bg-white px-4 py-12 text-center">
      <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
      <Button label="Back" severity="secondary" outlined @click="router.back()" />
    </div>

    <template v-else-if="ps">
      <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-[22px] font-bold">Payslip — {{ ps.employee_name }}</h1>
        <div class="flex gap-2">
          <Button label="Back to pay run" severity="secondary" outlined @click="router.push(`/payroll/run/${ps.pay_run_id}`)" />
          <Button label="Edit Payslip" severity="secondary" outlined @click="router.push(`/payroll/payslips/${ps.id}/edit`)" />
        </div>
      </div>

      <div class="mx-auto max-w-3xl rounded-[5px] border border-fa-border bg-white p-6">
        <div class="mb-4 flex flex-wrap gap-x-10 gap-y-1 text-sm">
          <div><span class="text-fa-muted">Tax Code:</span> <span class="font-semibold">{{ ps.tax_code || '—' }}</span></div>
          <div><span class="text-fa-muted">NI Category:</span> <span class="font-semibold">{{ ps.ni_category_letter }}</span></div>
          <div>
            <span class="text-fa-muted">NI Calculated as:</span>
            <span class="font-semibold capitalize">{{ ps.nic_calculation.replace('_', ' ') }}</span>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-6 md:grid-cols-3">
          <!-- Payments -->
          <div>
            <h2 class="mb-2 border-b border-fa-border pb-1 text-[13px] font-semibold text-fa-muted">Payments</h2>
            <dl class="space-y-1 text-sm">
              <div v-for="[label, value] in payments" :key="label" class="flex justify-between">
                <dt>{{ label }}</dt>
                <dd>{{ formatMoney(value) }}</dd>
              </div>
            </dl>
          </div>

          <!-- Deductions -->
          <div>
            <h2 class="mb-2 border-b border-fa-border pb-1 text-[13px] font-semibold text-fa-muted">Deductions</h2>
            <dl class="space-y-1 text-sm">
              <div v-for="[label, value] in deductions" :key="label" class="flex justify-between">
                <dt>{{ label }}</dt>
                <dd>{{ formatMoney(value) }}</dd>
              </div>
              <div v-if="deductions.length === 0" class="text-fa-muted">None</div>
            </dl>
          </div>

          <!-- Year to date -->
          <div>
            <h2 class="mb-2 border-b border-fa-border pb-1 text-[13px] font-semibold text-fa-muted">Year to Date</h2>
            <dl class="space-y-1 text-sm">
              <div class="flex justify-between"><dt>Gross Pay</dt><dd>{{ formatMoney(ps.year_to_date.gross_pay) }}</dd></div>
              <div class="flex justify-between"><dt>Tax Paid</dt><dd>{{ formatMoney(ps.year_to_date.tax_deducted) }}</dd></div>
              <div class="flex justify-between"><dt>Employee NI</dt><dd>{{ formatMoney(ps.year_to_date.employee_ni) }}</dd></div>
              <div class="flex justify-between"><dt>Employer NI</dt><dd>{{ formatMoney(ps.year_to_date.employer_ni) }}</dd></div>
              <div class="flex justify-between"><dt>Net Pay</dt><dd>{{ formatMoney(ps.year_to_date.net_pay) }}</dd></div>
            </dl>
          </div>
        </div>

        <div class="mt-6 flex justify-between border-t border-fa-border pt-4">
          <div class="text-sm">
            <span class="text-fa-muted">Total payments</span>
            <span class="ml-2 font-bold">{{ formatMoney(ps.gross_pay) }}</span>
          </div>
          <div class="text-sm">
            <span class="text-fa-muted">Net pay</span>
            <span class="ml-2 text-[18px] font-bold">{{ formatMoney(ps.net_pay) }}</span>
          </div>
        </div>
      </div>
    </template>
  </AppLayout>
</template>
