<script setup lang="ts">
// New expense form — wired to POST /api/v1/expenses.
// Supported fields are live; the Attachment / VAT / Project / Recurring
// sections are kept for layout but disabled ("coming soon") — they have no
// backend yet, so they are never sent.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import DatePicker from 'primevue/datepicker'
import RadioButton from 'primevue/radiobutton'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { listCategories, createExpense } from '@/services/expenses.service'
import { toISODate } from '@/lib/format'
import type { ExpenseCategory, CreateExpenseRequest } from '@/types/expense'
import type { ApiError } from '@/lib/api'

const router = useRouter()

// --- reference data: categories (for the picker) ---
const categories = ref<ExpenseCategory[]>([])
const categoriesLoading = ref(true)
const categoriesError = ref('')
const categoryOptions = computed(() =>
  categories.value.map((c) => ({ label: `${c.name} (${c.nominal_code})`, value: c.id })),
)

async function loadCategories() {
  categoriesLoading.value = true
  categoriesError.value = ''
  try {
    categories.value = await listCategories()
  } catch (err) {
    categoriesError.value = (err as ApiError)?.message ?? 'Could not load categories.'
  } finally {
    categoriesLoading.value = false
  }
}
onMounted(loadCategories)

// --- wired form state ---
const form = reactive({
  category: '',
  datedOn: new Date() as Date | null, // default to today
  currency: 'GBP',
  totalValue: '',
  description: '',
  supplierName: '',
  supplierVat: '',
  invoiceNumber: '',
  receiptReference: '',
})
const currencyOptions = ['GBP', 'EUR', 'USD']
const currencySymbols: Record<string, string> = { GBP: '£', EUR: '€', USD: '$' }
const currencySymbol = computed(() => currencySymbols[form.currency] ?? '')

// --- disabled "coming soon" sections — kept for layout, never sent ---
const attachmentDesc = ref('')
const vatOption = ref('uk')
const vatRate = ref('Standard 20%')
const vatRateOptions = ['Standard 20%', 'Reduced 5%', 'Zero 0%', 'Exempt']
const project = ref('-- None --')
const recurrence = ref('-- Does Not Recur --')

// --- validation ---
const errors = reactive<Record<string, string>>({})
// Positive decimal with up to 2 dp. The backend truncates beyond 2 dp, so we
// guard here to avoid a silent change to the stored amount.
const MONEY_RE = /^\d+(\.\d{1,2})?$/

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.category) errors.category = 'Please choose a category.'
  if (!form.datedOn) errors.datedOn = 'Please choose a date.'
  if (!form.description.trim()) errors.description = 'Please enter a description.'
  const tv = form.totalValue.trim()
  if (!tv) {
    errors.totalValue = 'Please enter an amount.'
  } else if (!MONEY_RE.test(tv)) {
    errors.totalValue = 'Enter a positive amount with up to 2 decimal places.'
  } else if (parseFloat(tv) <= 0) {
    errors.totalValue = 'Amount must be greater than zero.'
  }
  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')
const successMessage = ref('')

function buildPayload(): CreateExpenseRequest {
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  return {
    category_id: form.category,
    dated_on: toISODate(form.datedOn as Date),
    description: form.description.trim(),
    gross_value: form.totalValue.trim(),
    currency: form.currency,
    supplier_name: opt(form.supplierName),
    supplier_vat_number: opt(form.supplierVat),
    invoice_number: opt(form.invoiceNumber),
    receipt_reference: opt(form.receiptReference),
  }
}

function resetForm() {
  form.category = ''
  form.datedOn = new Date()
  form.currency = 'GBP'
  form.totalValue = ''
  form.description = ''
  form.supplierName = ''
  form.supplierVat = ''
  form.invoiceNumber = ''
  form.receiptReference = ''
  for (const k of Object.keys(errors)) delete errors[k]
}

async function submit(addAnother: boolean) {
  if (submitting.value) return
  formError.value = ''
  successMessage.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    const created = await createExpense(buildPayload())
    if (addAnother) {
      resetForm()
      successMessage.value = 'Expense created. Add another below.'
    } else {
      // Show the freshly created expense (its enriched detail).
      router.push(`/expenses/${created.id}`)
    }
  } catch (err) {
    // 401 is already handled by apiFetch. 422 / 400 / 500 land here.
    formError.value = (err as ApiError)?.message ?? 'Could not create the expense. Please try again.'
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push('/expenses')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">New Out-of-Pocket Expense</h1>

    <div
      v-if="formError"
      class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
      role="alert"
    >
      {{ formError }}
    </div>
    <div
      v-if="successMessage"
      class="mb-4 rounded border border-[#cfe9c7] bg-[#eaf7e6] px-3 py-2 text-sm text-[#3f8038]"
      role="status"
    >
      {{ successMessage }}
    </div>

    <!-- Attachment — coming soon (disabled) -->
    <FaCard title="Attachment" note="Coming soon">
      <FormRow label="File to attach">
        <div class="flex items-center gap-2.5 opacity-60">
          <Button label="Upload a file" severity="secondary" outlined disabled />
          <span class="text-sm text-fa-muted">or</span>
          <span class="text-sm text-fa-muted"><i class="pi pi-bolt" /> Upload via Smart Capture</span>
        </div>
      </FormRow>
      <FormRow label="Attachment description" label-for="att-desc">
        <InputText id="att-desc" v-model="attachmentDesc" class="w-72" disabled />
      </FormRow>
    </FaCard>

    <FaCard title="Expense details" note="Required fields *">
      <FormRow label="Category" label-for="category" required>
        <Select
          id="category"
          v-model="form.category"
          :options="categoryOptions"
          option-label="label"
          option-value="value"
          :placeholder="categoriesLoading ? 'Loading…' : 'Select a category'"
          :loading="categoriesLoading"
          :invalid="!!errors.category"
          class="w-72"
        />
        <p v-if="categoriesError" class="text-xs text-[#c0392b]">
          {{ categoriesError }}
          <button type="button" class="underline" @click="loadCategories">Retry</button>
        </p>
        <p v-if="errors.category" class="text-xs text-[#c0392b]">{{ errors.category }}</p>
      </FormRow>

      <FormRow label="Dated" label-for="dated" required>
        <DatePicker
          id="dated"
          v-model="form.datedOn"
          date-format="dd M yy"
          show-icon
          :show-on-focus="false"
          :invalid="!!errors.datedOn"
        />
        <p v-if="errors.datedOn" class="text-xs text-[#c0392b]">{{ errors.datedOn }}</p>
      </FormRow>

      <FormRow label="Currency" label-for="currency" required>
        <Select id="currency" v-model="form.currency" :options="currencyOptions" class="w-40" />
      </FormRow>

      <FormRow label="Total value" label-for="total" required>
        <!-- Money is entered as text (never a numeric/float input) and validated
             as a positive decimal with ≤2 dp before sending. -->
        <InputGroup class="w-56">
          <InputGroupAddon>{{ currencySymbol }}</InputGroupAddon>
          <InputText
            id="total"
            v-model="form.totalValue"
            placeholder="0.00"
            inputmode="decimal"
            :invalid="!!errors.totalValue"
          />
        </InputGroup>
        <p v-if="errors.totalValue" class="text-xs text-[#c0392b]">{{ errors.totalValue }}</p>
      </FormRow>

      <!-- VAT — coming soon (disabled) -->
      <FormRow label="VAT options">
        <label class="inline-flex items-center gap-2 text-sm text-fa-muted">
          <RadioButton v-model="vatOption" value="uk" input-id="vat-uk" disabled /><span>UK VAT Rates</span>
        </label>
        <label class="inline-flex items-center gap-2 text-sm text-fa-muted">
          <RadioButton v-model="vatOption" value="reverse" input-id="vat-rev" disabled /><span>Reverse Charge</span>
        </label>
      </FormRow>
      <FormRow label="VAT rate" label-for="vatrate">
        <Select id="vatrate" v-model="vatRate" :options="vatRateOptions" class="w-56" disabled />
        <p class="text-xs text-fa-muted">VAT handling is coming soon.</p>
      </FormRow>

      <FormRow label="Description" label-for="description" required>
        <InputText
          id="description"
          v-model="form.description"
          class="w-full max-w-xl"
          :invalid="!!errors.description"
        />
        <p v-if="errors.description" class="text-xs text-[#c0392b]">{{ errors.description }}</p>
      </FormRow>

      <FormRow label="Supplier name" label-for="supplier">
        <InputText id="supplier" v-model="form.supplierName" class="w-72" />
      </FormRow>

      <FormRow label="Supplier VAT number" label-for="supplier-vat">
        <InputText id="supplier-vat" v-model="form.supplierVat" class="w-56" />
      </FormRow>

      <FormRow label="Invoice number" label-for="invoice">
        <InputText id="invoice" v-model="form.invoiceNumber" class="w-56" />
      </FormRow>

      <FormRow label="Receipt reference" label-for="receipt">
        <InputText id="receipt" v-model="form.receiptReference" class="w-40" />
      </FormRow>
    </FaCard>

    <!-- Project — coming soon (disabled) -->
    <FaCard title="Is this a project expense?" note="Coming soon">
      <FormRow label="Link to project" label-for="project">
        <Select id="project" v-model="project" :options="['-- None --']" class="w-72" disabled />
      </FormRow>
    </FaCard>

    <!-- Recurring — coming soon (disabled) -->
    <FaCard title="Recurring options" note="Coming soon">
      <FormRow label="This expense recurs" label-for="recurs">
        <Select id="recurs" v-model="recurrence" :options="['-- Does Not Recur --']" class="w-72" disabled />
      </FormRow>
    </FaCard>

    <div class="flex items-center gap-3 py-2 pb-6">
      <Button label="Create new expense" :loading="submitting" @click="submit(false)" />
      <Button
        label="Create and add another"
        severity="secondary"
        outlined
        :disabled="submitting"
        @click="submit(true)"
      />
      <button type="button" class="font-semibold text-fa-blue hover:underline" @click="cancel">
        Cancel
      </button>
    </div>
  </AppLayout>
</template>
