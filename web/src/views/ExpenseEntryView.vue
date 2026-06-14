<script setup lang="ts">
// Expense form — DUAL-MODE:
//   /expenses/new        → create  (POST /api/v1/expenses)
//   /expenses/:id/edit   → edit    (PUT  /api/v1/expenses/:id), pre-filled from the record
// Supported fields are live (incl. VAT rate + amount); the Attachment / Project /
// Recurring sections + the VAT-options radios stay disabled ("coming soon").
import { ref, reactive, computed, watch, onMounted, nextTick } from 'vue'
import { useRouter, useRoute } from 'vue-router'
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
import AttachmentsField from '@/components/AttachmentsField.vue'
import { listCategories, listVatRates, createExpense, getExpense, updateExpense } from '@/services/expenses.service'
import { toISODate, computeFixedVatPounds } from '@/lib/format'
import type { ExpenseCategory, VatRate, CreateExpenseRequest } from '@/types/expense'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const route = useRoute()

// Edit mode iff we're on /expenses/:id/edit (the create route has no :id param).
const editId = typeof route.params.id === 'string' ? route.params.id : undefined
const isEdit = !!editId

// The staged-attachments manager. We call its commit() after the expense exists
// (create) or has been saved (edit) to apply the file changes.
const attachments = ref<InstanceType<typeof AttachmentsField> | null>(null)

// Positive decimal with up to 2 dp. The backend truncates beyond 2 dp, so we
// guard here to avoid a silent change to the stored amount.
const MONEY_RE = /^\d+(\.\d{1,2})?$/

// Custom-VAT cap (% of the total), configurable at build time via the env var.
const parsedCap = Number(import.meta.env.VITE_VAT_MAX_PERCENT)
const VAT_MAX_PERCENT = Number.isFinite(parsedCap) && parsedCap > 0 ? parsedCap : 30

// --- reference data: categories ---
const categories = ref<ExpenseCategory[]>([])
const categoriesLoading = ref(true)
const categoriesError = ref('')
// Categories grouped by their category_group; PrimeVue renders the group as a
// non-selectable header. Backend already orders by group then nominal code.
const categoryGroups = computed(() => {
  const groups = new Map<string, { label: string; value: string }[]>()
  for (const c of categories.value) {
    const groupName = c.category_group ?? 'Other'
    if (!groups.has(groupName)) groups.set(groupName, [])
    groups.get(groupName)!.push({ label: `${c.name} (${c.nominal_code})`, value: c.id })
  }
  return [...groups.entries()].map(([group, items]) => ({ group, items }))
})

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

// --- reference data: VAT rates ---
const vatRates = ref<VatRate[]>([])
const vatRatesLoading = ref(true)
const vatRatesError = ref('')
const vatRateOptions = computed(() =>
  vatRates.value.map((r) => ({ label: `${r.name} (${r.rate})`, value: r.id })),
)

async function loadVatRates() {
  vatRatesLoading.value = true
  vatRatesError.value = ''
  try {
    vatRates.value = await listVatRates()
  } catch (err) {
    vatRatesError.value = (err as ApiError)?.message ?? 'Could not load VAT rates.'
  } finally {
    vatRatesLoading.value = false
  }
}

// --- wired form state ---
const form = reactive({
  category: '',
  datedOn: new Date() as Date | null, // default to today (create)
  currency: 'GBP',
  totalValue: '',
  vatRate: '',
  vatAmount: '',
  description: '',
  supplierName: '',
  supplierVat: '',
  invoiceNumber: '',
  receiptReference: '',
})
const currencyOptions = ['GBP', 'EUR', 'USD']
const currencySymbols: Record<string, string> = { GBP: '£', EUR: '€', USD: '$' }
const currencySymbol = computed(() => currencySymbols[form.currency] ?? '')

const selectedVatRate = computed(() => vatRates.value.find((r) => r.id === form.vatRate) ?? null)
const isFixedRatio = computed(() => selectedVatRate.value?.is_fixed_ratio ?? false)

// True while we pre-fill the form in edit mode, so the VAT watch below doesn't
// wipe/recompute the loaded amount mid-hydration.
const hydrating = ref(false)

// For a fixed-ratio rate, the VAT amount is derived from the (VAT-inclusive)
// total and locked. Recompute whenever the rate or total changes; clear it when
// switching to a custom rate so the user types their own.
watch([() => form.vatRate, () => form.totalValue], ([newRate], [oldRate]) => {
  if (hydrating.value) return
  const rate = selectedVatRate.value
  if (rate?.is_fixed_ratio) {
    form.vatAmount = computeFixedVatPounds(form.totalValue, rate.rate_bps)
  } else if (newRate !== oldRate) {
    form.vatAmount = ''
  }
})

// Live red remark for a custom VAT amount (format + cap). Empty until invalid.
const vatAmountLiveError = computed(() => {
  const rate = selectedVatRate.value
  if (!rate || rate.is_fixed_ratio) return ''
  const va = form.vatAmount.trim()
  if (!va) return ''
  if (!MONEY_RE.test(va)) return 'Enter a VAT amount with up to 2 decimal places.'
  const total = Number(form.totalValue)
  if (Number.isFinite(total) && total > 0) {
    const cap = total * (VAT_MAX_PERCENT / 100)
    if (Number(va) > cap) {
      return `VAT amount can't exceed ${VAT_MAX_PERCENT}% of the total (${currencySymbol.value}${cap.toFixed(2)}).`
    }
  }
  return ''
})

// --- disabled "coming soon" sections — kept for layout, never sent ---
const vatOption = ref('uk')
const project = ref('-- None --')
const recurrence = ref('-- Does Not Recur --')

// --- edit-mode load state ---
const loadingExpense = ref(isEdit) // show a spinner until the record + refs are ready
const loadError = ref('')
const notEditable = ref(false)

async function loadForEdit() {
  if (!editId) return
  loadingExpense.value = true
  loadError.value = ''
  notEditable.value = false
  try {
    // Reference data FIRST so the pre-selected options exist on the dropdowns.
    await Promise.all([loadCategories(), loadVatRates()])
    const exp = await getExpense(editId)
    if (exp.status !== 'DRAFT' && exp.status !== 'REJECTED') {
      notEditable.value = true
      return
    }
    hydrating.value = true
    form.category = exp.category_id
    form.datedOn = new Date(`${exp.dated_on}T00:00:00`) // local midnight, no tz shift
    form.currency = exp.currency
    form.totalValue = exp.gross_value
    form.vatRate = exp.vat_rate_id ?? ''
    form.vatAmount = exp.vat_value
    form.description = exp.description
    form.supplierName = exp.supplier_name ?? ''
    form.supplierVat = exp.supplier_vat_number ?? ''
    form.invoiceNumber = exp.invoice_number ?? ''
    form.receiptReference = exp.receipt_reference ?? ''
    await nextTick()
    hydrating.value = false
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this expense.'
  } finally {
    loadingExpense.value = false
  }
}

onMounted(() => {
  if (isEdit) {
    loadForEdit()
  } else {
    loadCategories()
    loadVatRates()
  }
})

// --- validation ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.category) errors.category = 'Please choose a category.'
  if (!form.datedOn) errors.datedOn = 'Please choose a date.'
  if (!form.description.trim()) errors.description = 'Please enter a description.'

  const tv = form.totalValue.trim()
  if (!tv) errors.totalValue = 'Please enter an amount.'
  else if (!MONEY_RE.test(tv)) errors.totalValue = 'Enter a positive amount with up to 2 decimal places.'
  else if (Number(tv) <= 0) errors.totalValue = 'Amount must be greater than zero.'

  // VAT rate is required. For a custom (non-fixed-ratio) rate, validate the
  // user-entered amount (fixed-ratio amounts are auto-calculated).
  if (!form.vatRate) {
    errors.vatRate = 'Please choose a VAT rate.'
  } else if (selectedVatRate.value && !selectedVatRate.value.is_fixed_ratio) {
    const va = form.vatAmount.trim()
    const total = Number(form.totalValue)
    if (!va) {
      errors.vatAmount = 'Please enter the VAT amount.'
    } else if (!MONEY_RE.test(va)) {
      errors.vatAmount = 'Enter a VAT amount with up to 2 decimal places.'
    } else if (Number.isFinite(total) && total > 0 && Number(va) > total * (VAT_MAX_PERCENT / 100)) {
      const cap = (total * (VAT_MAX_PERCENT / 100)).toFixed(2)
      errors.vatAmount = `VAT amount can't exceed ${VAT_MAX_PERCENT}% of the total (${currencySymbol.value}${cap}).`
    }
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
    // VAT rate is the "type"; the amount is auto for fixed-ratio (backend
    // recomputes + ignores it) or the user's value for a custom rate.
    vat_rate_id: form.vatRate || undefined,
    vat_amount: opt(form.vatAmount),
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
  form.vatRate = ''
  form.vatAmount = ''
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
    if (editId) {
      // Save the expense fields first; if that fails we never touch attachments.
      await updateExpense(editId, buildPayload())
      // Then apply the staged attachment changes (delete / upload / set-primary).
      const result = await attachments.value?.commit(editId)
      if (result && !result.ok) {
        // Fields saved, but some attachment changes failed. Stay on the form so
        // the user can retry — the field already re-synced to server truth.
        formError.value = `Your changes were saved, but the attachments didn't fully update: ${result.message}`
        return
      }
      router.push(`/expenses/${editId}`)
    } else {
      const created = await createExpense(buildPayload())
      // The expense now exists, so upload the staged files to it.
      const result = await attachments.value?.commit(created.id)
      const filesFailed = !!(result && !result.ok)
      if (addAnother) {
        resetForm()
        attachments.value?.reset() // clear the file list for the next expense
        successMessage.value = filesFailed
          ? 'Expense created, but some files didn’t upload — add them by editing it.'
          : 'Expense created. Add another below.'
      } else if (filesFailed) {
        // Expense saved; some receipts didn't upload. Land on the detail with a
        // notice (the user can finish attaching from the edit page).
        router.push({ path: `/expenses/${created.id}`, query: { attach: 'partial' } })
      } else {
        router.push(`/expenses/${created.id}`)
      }
    }
  } catch (err) {
    // 401 is already handled by apiFetch. 409 (no longer editable) / 422 / 500 land here.
    formError.value =
      (err as ApiError)?.message ??
      (editId ? 'Could not save your changes. Please try again.' : 'Could not create the expense. Please try again.')
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push(editId ? `/expenses/${editId}` : '/expenses')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">
      {{ isEdit ? 'Edit expense' : 'New Out-of-Pocket Expense' }}
    </h1>

    <!-- Edit: loading -->
    <FaCard v-if="isEdit && loadingExpense" title="Expense details">
      <div class="py-10 text-center text-fa-muted"><i class="pi pi-spin pi-spinner mr-2" />Loading…</div>
    </FaCard>

    <!-- Edit: not editable (status other than DRAFT/REJECTED) -->
    <FaCard v-else-if="isEdit && notEditable" title="Can't edit this expense">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-fa-muted">Only DRAFT or REJECTED expenses can be edited.</p>
        <Button label="Back to expense" severity="secondary" outlined @click="router.push(`/expenses/${editId}`)" />
      </div>
    </FaCard>

    <!-- Edit: load error -->
    <FaCard v-else-if="isEdit && loadError" title="Expense details">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Back to list" severity="secondary" outlined @click="router.push('/expenses')" />
      </div>
    </FaCard>

    <!-- The form (create, or edit once the record loaded ok) -->
    <template v-else>
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

      <!-- Receipts — multi-file upload, staged until save (see AttachmentsField). -->
      <AttachmentsField ref="attachments" :expense-id="editId" />

      <FaCard title="Expense details" note="Required fields *">
        <FormRow label="Category" label-for="category" required>
          <Select
            id="category"
            v-model="form.category"
            :options="categoryGroups"
            option-group-label="group"
            option-group-children="items"
            option-label="label"
            option-value="value"
            :placeholder="categoriesLoading ? 'Loading…' : 'Select a category'"
            :loading="categoriesLoading"
            :invalid="!!errors.category"
            filter
            filter-placeholder="Search categories"
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

        <!-- VAT options — coming soon (disabled) -->
        <FormRow label="VAT options">
          <label class="inline-flex items-center gap-2 text-sm text-fa-muted">
            <RadioButton v-model="vatOption" value="uk" input-id="vat-uk" disabled /><span>UK VAT Rates</span>
          </label>
          <label class="inline-flex items-center gap-2 text-sm text-fa-muted">
            <RadioButton v-model="vatOption" value="reverse" input-id="vat-rev" disabled /><span>Reverse Charge</span>
          </label>
        </FormRow>

        <FormRow label="VAT rate" label-for="vatrate" required>
          <Select
            id="vatrate"
            v-model="form.vatRate"
            :options="vatRateOptions"
            option-label="label"
            option-value="value"
            :placeholder="vatRatesLoading ? 'Loading…' : 'Select a VAT rate'"
            :loading="vatRatesLoading"
            :invalid="!!errors.vatRate"
            class="w-56"
          />
          <p v-if="vatRatesError" class="text-xs text-[#c0392b]">
            {{ vatRatesError }}
            <button type="button" class="underline" @click="loadVatRates">Retry</button>
          </p>
          <p v-if="errors.vatRate" class="text-xs text-[#c0392b]">{{ errors.vatRate }}</p>
        </FormRow>

        <FormRow label="VAT amount" label-for="vatamount" required>
          <InputGroup class="w-56">
            <InputGroupAddon>{{ currencySymbol }}</InputGroupAddon>
            <InputText
              id="vatamount"
              v-model="form.vatAmount"
              placeholder="0.00"
              inputmode="decimal"
              :disabled="isFixedRatio || !form.vatRate"
              :invalid="!!errors.vatAmount || !!vatAmountLiveError"
            />
          </InputGroup>
          <p v-if="isFixedRatio" class="text-xs text-fa-muted">
            Calculated automatically from the VAT-inclusive total.
          </p>
          <p v-if="errors.vatAmount || vatAmountLiveError" class="text-xs text-[#c0392b]">
            {{ errors.vatAmount || vatAmountLiveError }}
          </p>
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
        <Button
          v-if="isEdit"
          label="Save changes"
          :loading="submitting"
          @click="submit(false)"
        />
        <template v-else>
          <Button label="Create new expense" :loading="submitting" @click="submit(false)" />
          <Button
            label="Create and add another"
            severity="secondary"
            outlined
            :disabled="submitting"
            @click="submit(true)"
          />
        </template>
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
