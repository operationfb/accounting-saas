<script setup lang="ts">
// Edit Payslip — GET then PUT /api/v1/payroll/payslips/:id. The full Edit Payslip
// form (Pay, Statutory Pay, Deductions, Tax & NI). Saving recomputes tax/NI on the
// backend; we navigate back to the pay run. DRAFT runs only (a 409 surfaces as the
// error). Owner/admin only.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { getPayslip, updatePayslip } from '@/services/payroll.service'
import type { Payslip, UpdatePayslipRequest } from '@/types/payroll'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const payRunId = ref('')
const employeeName = ref('')

const id = computed(() => route.params.id as string)

// The editable form — all money fields are pound strings.
const form = reactive<UpdatePayslipRequest>({
  tax_code: '',
  ni_category_letter: 'A',
  nic_calculation: 'employee',
  week1_month1_basis: false,
  student_loan_undergraduate: false,
  student_loan_postgraduate: false,
  basic_pay: '0.00',
  overtime: '0.00',
  bonus: '0.00',
  commission: '0.00',
  allowance: '0.00',
  absence_payments: '0.00',
  holiday_pay: '0.00',
  other_payments: '0.00',
  pay_not_subject_to_tax_ni: '0.00',
  statutory_sick_pay: '0.00',
  statutory_maternity_pay: '0.00',
  statutory_paternity_pay: '0.00',
  statutory_adoption_pay: '0.00',
  shared_parental_pay: '0.00',
  statutory_neonatal_care_pay: '0.00',
  statutory_parental_bereavement_pay: '0.00',
  payroll_giving: '0.00',
  other_deductions_net_pay: '0.00',
  items_class1_nic_not_paye: '0.00',
  salary_sacrifice_deductions: '0.00',
  hours_worked: '',
  comment: '',
})

const niCategories = ['A', 'B', 'C', 'H', 'J', 'M', 'Z']
const nicCalculations = [
  { value: 'director', label: 'Director' },
  { value: 'director_alternative', label: 'Director (alternative arrangements)' },
  { value: 'employee', label: 'Employee' },
]

const payFields: [keyof UpdatePayslipRequest, string][] = [
  ['basic_pay', 'Basic Pay'],
  ['overtime', 'Overtime'],
  ['bonus', 'Bonus'],
  ['commission', 'Commission'],
  ['allowance', 'Allowance'],
  ['absence_payments', 'Absence Payments'],
  ['holiday_pay', 'Holiday Pay'],
  ['other_payments', 'Other Payments'],
  ['pay_not_subject_to_tax_ni', 'Pay Not Subject to Tax or NI'],
]
const statutoryFields: [keyof UpdatePayslipRequest, string][] = [
  ['statutory_sick_pay', 'Statutory Sick Pay'],
  ['statutory_maternity_pay', 'Statutory Maternity Pay'],
  ['statutory_paternity_pay', 'Statutory Paternity Pay'],
  ['statutory_adoption_pay', 'Statutory Adoption Pay'],
  ['shared_parental_pay', 'Shared Parental Pay'],
  ['statutory_neonatal_care_pay', 'Statutory Neonatal Care Pay'],
  ['statutory_parental_bereavement_pay', 'Statutory Parental Bereavement Pay'],
]
const deductionFields: [keyof UpdatePayslipRequest, string][] = [
  ['payroll_giving', 'Payroll Giving'],
  ['other_deductions_net_pay', 'Other Deductions From Net Pay'],
  ['items_class1_nic_not_paye', 'Items Subject to Class 1 NIC but not PAYE'],
  ['salary_sacrifice_deductions', 'Salary Sacrifice Deductions'],
]

function hydrate(ps: Payslip) {
  payRunId.value = ps.pay_run_id
  employeeName.value = ps.employee_name
  form.tax_code = ps.tax_code ?? ''
  form.ni_category_letter = ps.ni_category_letter
  form.nic_calculation = ps.nic_calculation
  form.week1_month1_basis = ps.week1_month1_basis
  form.student_loan_undergraduate = ps.student_loan_undergraduate
  form.student_loan_postgraduate = ps.student_loan_postgraduate
  for (const [key] of [...payFields, ...statutoryFields, ...deductionFields]) {
    ;(form as Record<string, unknown>)[key] = (ps as Record<string, unknown>)[key]
  }
  form.hours_worked = ps.hours_worked ?? ''
  form.comment = ps.comment ?? ''
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    hydrate(await getPayslip(id.value))
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load the payslip.'
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    await updatePayslip(id.value, { ...form })
    router.push(`/payroll/run/${payRunId.value}`)
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not save the payslip.'
    saving.value = false
  }
}
onMounted(load)
</script>

<template>
  <AppLayout>
    <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading…
    </div>
    <template v-else>
      <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-[22px] font-bold">Edit Payslip — {{ employeeName }}</h1>
        <div class="flex gap-2">
          <Button label="Cancel" severity="secondary" outlined @click="router.push(`/payroll/run/${payRunId}`)" />
          <Button label="Save changes" :loading="saving" @click="save" />
        </div>
      </div>

      <p v-if="error" class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>

      <div class="mx-auto max-w-3xl space-y-5">
        <!-- Pay -->
        <section class="rounded-[5px] border border-fa-border bg-white p-5">
          <h2 class="mb-3 text-[15px] font-semibold">Pay</h2>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label v-for="[key, label] in payFields" :key="key" class="block text-sm">
              <span class="mb-1 block text-fa-muted">{{ label }}</span>
              <input v-model="(form as any)[key]" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none" inputmode="decimal" />
            </label>
          </div>
        </section>

        <!-- Statutory Pay -->
        <section class="rounded-[5px] border border-fa-border bg-white p-5">
          <h2 class="mb-3 text-[15px] font-semibold">Statutory Pay</h2>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label v-for="[key, label] in statutoryFields" :key="key" class="block text-sm">
              <span class="mb-1 block text-fa-muted">{{ label }}</span>
              <input v-model="(form as any)[key]" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none" inputmode="decimal" />
            </label>
          </div>
        </section>

        <!-- Deductions -->
        <section class="rounded-[5px] border border-fa-border bg-white p-5">
          <h2 class="mb-3 text-[15px] font-semibold">Deductions</h2>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label v-for="[key, label] in deductionFields" :key="key" class="block text-sm">
              <span class="mb-1 block text-fa-muted">{{ label }}</span>
              <input v-model="(form as any)[key]" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none" inputmode="decimal" />
            </label>
          </div>
        </section>

        <!-- Tax & NI -->
        <section class="rounded-[5px] border border-fa-border bg-white p-5">
          <h2 class="mb-3 text-[15px] font-semibold">Tax &amp; NI</h2>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="block text-sm">
              <span class="mb-1 block text-fa-muted">Tax Code</span>
              <input v-model="form.tax_code" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none" />
            </label>
            <label class="block text-sm">
              <span class="mb-1 block text-fa-muted">NI Category Letter</span>
              <select v-model="form.ni_category_letter" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none">
                <option v-for="c in niCategories" :key="c" :value="c">{{ c }}</option>
              </select>
            </label>
            <label class="block text-sm">
              <span class="mb-1 block text-fa-muted">NICs calculated as</span>
              <select v-model="form.nic_calculation" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none">
                <option v-for="n in nicCalculations" :key="n.value" :value="n.value">{{ n.label }}</option>
              </select>
            </label>
            <label class="block text-sm">
              <span class="mb-1 block text-fa-muted">Hours worked this month</span>
              <input v-model="form.hours_worked" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none" inputmode="decimal" />
            </label>
          </div>
          <div class="mt-3 flex flex-wrap gap-6 text-sm">
            <label class="flex items-center gap-2">
              <input v-model="form.week1_month1_basis" type="checkbox" /> Week 1 / Month 1 basis
            </label>
            <label class="flex items-center gap-2">
              <input v-model="form.student_loan_undergraduate" type="checkbox" /> Undergraduate loan
            </label>
            <label class="flex items-center gap-2">
              <input v-model="form.student_loan_postgraduate" type="checkbox" /> Postgraduate loan
            </label>
          </div>
        </section>

        <section class="rounded-[5px] border border-fa-border bg-white p-5">
          <label class="block text-sm">
            <span class="mb-1 block text-fa-muted">Comment</span>
            <textarea v-model="form.comment" rows="2" class="w-full rounded border border-fa-border px-2.5 py-1.5 text-sm focus:border-fa-blue focus:outline-none" />
          </label>
        </section>
      </div>
    </template>
  </AppLayout>
</template>
