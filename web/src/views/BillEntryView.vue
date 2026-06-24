<script setup lang="ts">
// Bill form — DUAL-MODE:
//   /bills/new        → create  (POST /api/v1/bills)
//   /bills/:id/edit   → edit    (PUT  /api/v1/bills/:id), pre-filled from the record
//
// Modelled on ExpenseEntryView (a single flat record) with the bill additions a
// supplier invoice needs: a supplier CONTACT, a reference, a due date, comments and
// a hire-purchase flag. VAT follows the EXPENSES pattern exactly: a vat_rate_id +
// a conditional vat_amount that's auto-computed & locked for a fixed-ratio rate and
// hand-entered for a non-fixed-ratio ("manual") rate.
//
// There is no status lifecycle: a bill is editable/deletable only while UNPAID
// (paid_value "0.00"). A paid bill loads READ-ONLY (all fields disabled, no Save/
// Delete) — the banking module owns paid_value.
import { ref, reactive, computed, watch, onMounted, nextTick } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import InputText from 'primevue/inputtext'
import Textarea from 'primevue/textarea'
import Select from 'primevue/select'
import DatePicker from 'primevue/datepicker'
import Checkbox from 'primevue/checkbox'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { createBill, getBill, updateBill, deleteBill, listBillCategories } from '@/services/bills.service'
import { listVatRates } from '@/services/expenses.service'
import { listContacts } from '@/services/contacts.service'
import { listProjects } from '@/services/projects.service'
import { listCurrencies } from '@/services/currencies.service'
import { toISODate, computeFixedVatPounds } from '@/lib/format'
import { buildCurrencyOptions, currencySymbolMap } from '@/lib/currency'
import type { CreateBillRequest, BillCategory } from '@/types/bill'
import type { VatRate } from '@/types/expense'
import type { Contact } from '@/types/contact'
import type { Project } from '@/types/project'
import type { Currency } from '@/types/currency'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const route = useRoute()

// Edit mode iff we're on /bills/:id/edit (the create route has no :id param).
const editId = typeof route.params.id === 'string' ? route.params.id : undefined
const isEdit = !!editId

// Money: a decimal with up to 2 dp, OPTIONALLY negative (a bill credit note / refund
// can be entered as a negative value, per the form note). The backend rounds beyond
// 2 dp; we guard here to avoid a silent change to the stored amount.
const MONEY_RE = /^-?\d+(\.\d{1,2})?$/

// Custom-VAT cap (% of the total), configurable at build time via the env var.
const parsedCap = Number(import.meta.env.VITE_VAT_MAX_PERCENT)
const VAT_MAX_PERCENT = Number.isFinite(parsedCap) && parsedCap > 0 ? parsedCap : 30

// --- reference data: supplier contacts ---
const contacts = ref<Contact[]>([])
const contactsLoading = ref(false)
const contactsError = ref('')
function contactDisplayName(c: Contact): string {
  const org = c.organisation_name?.trim()
  if (org) return org
  const person = `${c.first_name ?? ''} ${c.last_name ?? ''}`.trim()
  if (person) return person
  return c.email?.trim() || '(no name)'
}
const contactOptions = computed(() =>
  contacts.value.map((c) => ({ label: contactDisplayName(c), value: c.id })),
)
async function loadContacts() {
  contactsLoading.value = true
  contactsError.value = ''
  try {
    contacts.value = await listContacts()
  } catch (err) {
    contactsError.value = (err as ApiError)?.message ?? 'Could not load contacts.'
  } finally {
    contactsLoading.value = false
  }
}

// --- reference data: spending categories (the CoA spending subset) ---
const categories = ref<BillCategory[]>([])
const categoriesLoading = ref(true)
const categoriesError = ref('')
// CoA account_type → human-readable group heading. The backend already orders by
// account_type then nominal code.
const GROUP_LABELS: Record<string, string> = {
  COST_OF_SALES: 'Cost of Sales',
  ADMIN_EXPENSE: 'Admin Expenses',
  CAPITAL_ASSET: 'Capital Assets',
}
const categoryGroups = computed(() => {
  const groups = new Map<string, { label: string; value: string }[]>()
  for (const c of categories.value) {
    const groupName = GROUP_LABELS[c.account_type] ?? c.account_type
    if (!groups.has(groupName)) groups.set(groupName, [])
    groups.get(groupName)!.push({ label: `${c.name} (${c.nominal_code})`, value: c.id })
  }
  return [...groups.entries()].map(([group, items]) => ({ group, items }))
})
async function loadCategories() {
  categoriesLoading.value = true
  categoriesError.value = ''
  try {
    categories.value = await listBillCategories()
  } catch (err) {
    categoriesError.value = (err as ApiError)?.message ?? 'Could not load spending categories.'
  } finally {
    categoriesLoading.value = false
  }
}

// --- reference data: VAT rates (shared with expenses) ---
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

// --- reference data: projects (optional link) ---
const projects = ref<Project[]>([])
const projectsLoading = ref(false)
const projectsError = ref('')
async function loadProjects() {
  projectsLoading.value = true
  projectsError.value = ''
  try {
    projects.value = await listProjects()
  } catch (err) {
    projectsError.value = (err as ApiError)?.message ?? 'Could not load projects.'
  } finally {
    projectsLoading.value = false
  }
}

// --- wired form state ---
const form = reactive({
  contactId: '',
  reference: '',
  datedOn: new Date() as Date | null, // default to today (create)
  dueOn: null as Date | null, // optional
  currency: 'GBP',
  comments: '',
  isHirePurchase: false,
  categoryId: '',
  totalValue: '',
  vatRate: '',
  vatAmount: '',
  projectId: '', // '' = not a project bill
})

// "— None —" + the active projects. In edit mode, if the linked project isn't
// active, include it anyway so the selection still shows.
const projectOptions = computed(() => {
  const opts = [{ label: '— None —', value: '' }]
  const active = projects.value.filter((p) => p.status === 'active')
  for (const p of active) opts.push({ label: p.name, value: p.id })
  if (form.projectId && !active.some((p) => p.id === form.projectId)) {
    const linked = projects.value.find((p) => p.id === form.projectId)
    if (linked) opts.push({ label: linked.name, value: linked.id })
  }
  return opts
})

// --- reference data: currencies (the global ISO 4217 list) ---
const currencies = ref<Currency[]>([])
const currencyOptions = computed(() => buildCurrencyOptions(currencies.value))
const currencySymbol = computed(() => currencySymbolMap(currencies.value)[form.currency] ?? '')
async function loadCurrencies() {
  try {
    currencies.value = await listCurrencies()
  } catch {
    // Non-fatal: leave the picker empty. The GBP default still submits.
  }
}

// --- VAT computation (the expenses pattern) ---
const selectedVatRate = computed(() => vatRates.value.find((r) => r.id === form.vatRate) ?? null)
const isFixedRatio = computed(() => selectedVatRate.value?.is_fixed_ratio ?? false)

// True while we pre-fill the form in edit mode, so the VAT watch below doesn't
// wipe/recompute the loaded amount mid-hydration.
const hydrating = ref(false)

// For a fixed-ratio rate, the VAT amount is derived from the (VAT-inclusive) total
// and locked. Recompute whenever the rate or total changes; clear it when switching
// to a manual rate so the user types their own. (computeFixedVatPounds returns ''
// for a negative total — a credit note — but the backend re-extracts regardless.)
watch([() => form.vatRate, () => form.totalValue], ([newRate], [oldRate]) => {
  if (hydrating.value) return
  const rate = selectedVatRate.value
  if (rate?.is_fixed_ratio) {
    form.vatAmount = computeFixedVatPounds(form.totalValue, rate.rate_bps)
  } else if (newRate !== oldRate) {
    form.vatAmount = ''
  }
})

// Live red remark for a manual VAT amount (format + cap). Empty until invalid.
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

// --- edit-mode load state ---
const loadingBill = ref(isEdit) // show a spinner until the record + refs are ready
const loadError = ref('')
// A paid bill can't be edited (the backend returns 409). We still load + show it,
// but READ-ONLY: every control disabled, no Save/Delete.
const readonly = ref(false)

async function loadForEdit() {
  if (!editId) return
  loadingBill.value = true
  loadError.value = ''
  readonly.value = false
  try {
    // Reference data FIRST so the pre-selected options exist on the dropdowns.
    await Promise.all([loadContacts(), loadCategories(), loadVatRates(), loadProjects(), loadCurrencies()])
    const bill = await getBill(editId)
    hydrating.value = true
    form.contactId = bill.contact_id
    form.reference = bill.reference ?? ''
    form.datedOn = new Date(`${bill.dated_on}T00:00:00`) // local midnight, no tz shift
    form.dueOn = bill.due_on ? new Date(`${bill.due_on}T00:00:00`) : null
    form.currency = bill.currency
    form.comments = bill.comments ?? ''
    form.isHirePurchase = bill.is_hire_purchase
    form.categoryId = bill.category_id
    form.totalValue = bill.total_value
    form.vatRate = bill.vat_rate_id ?? ''
    form.vatAmount = bill.sales_tax_value // the stored VAT amount
    form.projectId = bill.project_id ?? ''
    // A bill with payments recorded against it is locked.
    if (bill.paid_value !== '0.00') readonly.value = true
    await nextTick()
    hydrating.value = false
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this bill.'
  } finally {
    loadingBill.value = false
  }
}

onMounted(() => {
  if (isEdit) {
    loadForEdit()
  } else {
    loadContacts()
    loadCategories()
    loadVatRates()
    loadProjects()
    loadCurrencies()
  }
})

// --- validation ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.contactId) errors.contactId = 'Please choose a supplier.'
  if (!form.reference.trim()) errors.reference = 'Please enter a reference.'
  if (!form.datedOn) errors.datedOn = 'Please choose a date.'
  if (!form.categoryId) errors.category = 'Please choose a spending category.'

  const tv = form.totalValue.trim()
  if (!tv) errors.totalValue = 'Please enter an amount.'
  else if (!MONEY_RE.test(tv)) errors.totalValue = 'Enter an amount with up to 2 decimal places.'

  // VAT is OPTIONAL. Validate the amount only for a manual (non-fixed-ratio) rate;
  // fixed-ratio amounts are auto-calculated, and no rate means no VAT.
  if (form.vatRate && selectedVatRate.value && !selectedVatRate.value.is_fixed_ratio) {
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

// --- submit / delete ---
const submitting = ref(false)
const deleting = ref(false)
const confirmingDelete = ref(false)
const formError = ref('')
const successMessage = ref('')

function buildPayload(): CreateBillRequest {
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  return {
    contact_id: form.contactId,
    reference: form.reference.trim(),
    dated_on: toISODate(form.datedOn as Date),
    due_on: form.dueOn ? toISODate(form.dueOn) : undefined,
    currency: form.currency,
    comments: opt(form.comments),
    is_hire_purchase: form.isHirePurchase,
    category_id: form.categoryId,
    total: form.totalValue.trim(),
    // VAT rate is the "type"; the amount is auto for a fixed-ratio rate (the backend
    // recomputes + ignores it) or the user's value for a manual rate.
    vat_rate_id: form.vatRate || undefined,
    vat_amount: opt(form.vatAmount),
    project_id: form.projectId || undefined,
  }
}

function resetForm() {
  form.contactId = ''
  form.reference = ''
  form.datedOn = new Date()
  form.dueOn = null
  form.currency = 'GBP'
  form.comments = ''
  form.isHirePurchase = false
  form.categoryId = ''
  form.totalValue = ''
  form.vatRate = ''
  form.vatAmount = ''
  form.projectId = ''
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
      await updateBill(editId, buildPayload())
      router.push('/bills')
    } else {
      await createBill(buildPayload())
      if (addAnother) {
        resetForm()
        successMessage.value = 'Bill created. Add another below.'
      } else {
        router.push('/bills')
      }
    }
  } catch (err) {
    // 401 is handled by apiFetch. 409 (paid → not editable) / 422 / 500 land here.
    formError.value =
      (err as ApiError)?.message ??
      (editId ? 'Could not save your changes. Please try again.' : 'Could not create the bill. Please try again.')
  } finally {
    submitting.value = false
  }
}

async function removeBill() {
  if (!editId || deleting.value) return
  deleting.value = true
  formError.value = ''
  try {
    await deleteBill(editId)
    router.push('/bills')
  } catch (err) {
    // 409 if a payment landed between load and delete.
    formError.value = (err as ApiError)?.message ?? 'Could not delete the bill.'
    confirmingDelete.value = false
    deleting.value = false
  }
}

function cancel() {
  router.push('/bills')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">{{ isEdit ? 'Edit bill' : 'New Bill' }}</h1>

    <!-- Edit: loading -->
    <FaCard v-if="isEdit && loadingBill" title="Bill details">
      <div class="py-10 text-center text-fa-muted"><i class="pi pi-spin pi-spinner mr-2" />Loading…</div>
    </FaCard>

    <!-- Edit: load error -->
    <FaCard v-else-if="isEdit && loadError" title="Bill details">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Back to bills" severity="secondary" outlined @click="router.push('/bills')" />
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
      <div
        v-if="readonly"
        class="mb-4 rounded border border-[#f3e2c0] bg-[#fcf6e9] px-3 py-2 text-sm text-[#8a6d1f]"
        role="status"
      >
        This bill has payments recorded against it and can't be edited or deleted.
      </div>

      <FaCard title="Bill Details" note="Required fields *">
        <FormRow label="Supplier" label-for="contact" required>
          <Select
            id="contact"
            v-model="form.contactId"
            :options="contactOptions"
            option-label="label"
            option-value="value"
            :placeholder="contactsLoading ? 'Loading…' : 'Search by name or organisation'"
            :loading="contactsLoading"
            :disabled="readonly"
            :invalid="!!errors.contactId"
            filter
            filter-placeholder="Search contacts"
            class="w-full sm:w-72"
          />
          <p v-if="contactsError" class="text-xs text-[#c0392b]">
            {{ contactsError }}
            <button type="button" class="underline" @click="loadContacts">Retry</button>
          </p>
          <p v-if="errors.contactId" class="text-xs text-[#c0392b]">{{ errors.contactId }}</p>
        </FormRow>

        <FormRow label="Reference" label-for="reference" required>
          <InputText
            id="reference"
            v-model="form.reference"
            class="w-full sm:w-72"
            :disabled="readonly"
            :invalid="!!errors.reference"
          />
          <p v-if="errors.reference" class="text-xs text-[#c0392b]">{{ errors.reference }}</p>
        </FormRow>

        <FormRow label="Bill date" label-for="dated" required>
          <DatePicker
            id="dated"
            v-model="form.datedOn"
            date-format="dd M yy"
            show-icon
            :show-on-focus="false"
            :disabled="readonly"
            :invalid="!!errors.datedOn"
            class="w-full sm:w-72"
          />
          <p v-if="errors.datedOn" class="text-xs text-[#c0392b]">{{ errors.datedOn }}</p>
        </FormRow>

        <FormRow label="Due on" label-for="due">
          <DatePicker
            id="due"
            v-model="form.dueOn"
            date-format="dd M yy"
            show-icon
            :show-on-focus="false"
            :disabled="readonly"
            class="w-full sm:w-72"
          />
        </FormRow>

        <FormRow label="Currency" label-for="currency">
          <Select
            id="currency"
            v-model="form.currency"
            :options="currencyOptions"
            option-label="label"
            option-value="value"
            option-disabled="disabled"
            :disabled="readonly"
            class="w-full sm:w-72"
          />
        </FormRow>

        <FormRow label="Comments" label-for="comments">
          <Textarea
            id="comments"
            v-model="form.comments"
            rows="3"
            auto-resize
            class="w-full max-w-xl"
            :disabled="readonly"
          />
        </FormRow>

        <FormRow label="Hire purchase" label-for="hp">
          <label class="inline-flex items-center gap-2 text-sm">
            <Checkbox v-model="form.isHirePurchase" input-id="hp" :binary="true" :disabled="readonly" />
            <span>This will be paid using a hire purchase agreement</span>
          </label>
        </FormRow>
      </FaCard>

      <FaCard title="Bill Contents">
        <FormRow label="Spending category" label-for="category" required>
          <Select
            id="category"
            v-model="form.categoryId"
            :options="categoryGroups"
            option-group-label="group"
            option-group-children="items"
            option-label="label"
            option-value="value"
            :placeholder="categoriesLoading ? 'Loading…' : 'Select a category'"
            :loading="categoriesLoading"
            :disabled="readonly"
            :invalid="!!errors.category"
            filter
            filter-placeholder="Search categories"
            scroll-height="380px"
            class="w-full sm:w-72"
          />
          <p v-if="categoriesError" class="text-xs text-[#c0392b]">
            {{ categoriesError }}
            <button type="button" class="underline" @click="loadCategories">Retry</button>
          </p>
          <p v-if="errors.category" class="text-xs text-[#c0392b]">{{ errors.category }}</p>
        </FormRow>

        <FormRow label="Total price (incl. VAT)" label-for="total" required>
          <!-- Money is entered as text (never a numeric/float input). A negative
               value is allowed — a bill credit note / refund. -->
          <InputGroup class="w-full sm:w-56">
            <InputGroupAddon>{{ currencySymbol }}</InputGroupAddon>
            <InputText
              id="total"
              v-model="form.totalValue"
              placeholder="0.00"
              inputmode="decimal"
              :disabled="readonly"
              :invalid="!!errors.totalValue"
            />
          </InputGroup>
          <p class="text-xs text-fa-muted">Refunds (credit notes) can be entered using a negative value.</p>
          <p v-if="errors.totalValue" class="text-xs text-[#c0392b]">{{ errors.totalValue }}</p>
        </FormRow>

        <FormRow label="VAT rate" label-for="vatrate">
          <Select
            id="vatrate"
            v-model="form.vatRate"
            :options="vatRateOptions"
            option-label="label"
            option-value="value"
            :placeholder="vatRatesLoading ? 'Loading…' : 'Select a VAT rate'"
            :loading="vatRatesLoading"
            :disabled="readonly"
            show-clear
            class="w-full sm:w-56"
          />
          <p v-if="vatRatesError" class="text-xs text-[#c0392b]">
            {{ vatRatesError }}
            <button type="button" class="underline" @click="loadVatRates">Retry</button>
          </p>
        </FormRow>

        <FormRow v-if="form.vatRate" label="VAT amount" label-for="vatamount">
          <InputGroup class="w-full sm:w-56">
            <InputGroupAddon>{{ currencySymbol }}</InputGroupAddon>
            <InputText
              id="vatamount"
              v-model="form.vatAmount"
              placeholder="0.00"
              inputmode="decimal"
              :disabled="readonly || isFixedRatio"
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
      </FaCard>

      <!-- Project — link this bill to a project (optional). -->
      <FaCard title="Is this a project bill?">
        <FormRow label="Link to project" label-for="project">
          <Select
            id="project"
            v-model="form.projectId"
            :options="projectOptions"
            option-label="label"
            option-value="value"
            :placeholder="projectsLoading ? 'Loading…' : '— None —'"
            :loading="projectsLoading"
            :disabled="readonly"
            filter
            filter-placeholder="Search projects"
            class="w-full sm:w-72"
          />
          <p v-if="projectsError" class="text-xs text-[#c0392b]">
            {{ projectsError }}
            <button type="button" class="underline" @click="loadProjects">Retry</button>
          </p>
        </FormRow>
      </FaCard>

      <div class="flex flex-wrap items-center gap-3 py-2 pb-6">
        <template v-if="!readonly">
          <Button v-if="isEdit" label="Save changes" :loading="submitting" @click="submit(false)" />
          <template v-else>
            <Button label="Create bill" :loading="submitting" @click="submit(false)" />
            <Button
              label="Create and add another"
              severity="secondary"
              outlined
              :disabled="submitting"
              @click="submit(true)"
            />
          </template>

          <!-- Delete (edit only): a light two-step confirm, no native dialog. -->
          <template v-if="isEdit">
            <template v-if="!confirmingDelete">
              <Button label="Delete" severity="danger" text :disabled="submitting" @click="confirmingDelete = true" />
            </template>
            <template v-else>
              <span class="text-sm text-fa-muted">Delete this bill?</span>
              <Button label="Yes, delete" severity="danger" :loading="deleting" @click="removeBill" />
              <Button label="Keep" severity="secondary" outlined :disabled="deleting" @click="confirmingDelete = false" />
            </template>
          </template>
        </template>

        <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">
          {{ readonly ? 'Back to bills' : 'Cancel' }}
        </button>
      </div>
    </template>
  </AppLayout>
</template>
