<script setup lang="ts">
// Project form — DUAL-MODE:
//   /projects/new       → create (POST /api/v1/projects)
//   /projects/:id/edit  → edit   (PUT  /api/v1/projects/:id), pre-filled from the record
// Modelled on ContactEntryView; the three cards mirror the FreeAgent "New Project"
// screen (Project / Time and money / More options). Money/time are kept as the
// strings the API parses — the backend converts pounds→pence and "H:MM"→minutes,
// so we never do that arithmetic here.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Checkbox from 'primevue/checkbox'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import DatePicker from 'primevue/datepicker'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { getProject, createProject, updateProject } from '@/services/projects.service'
import { listContacts } from '@/services/contacts.service'
import { toISODate } from '@/lib/format'
import type { Contact } from '@/types/contact'
import type { CreateProjectRequest } from '@/types/project'
import type { ApiError } from '@/lib/api'
import { listCurrencies } from '@/services/currencies.service'
import { buildCurrencyOptions } from '@/lib/currency'
import type { Currency } from '@/types/currency'

const router = useRouter()
const route = useRoute()

// Edit mode iff we're on /projects/:id/edit (the create route has no :id param).
const editId = typeof route.params.id === 'string' ? route.params.id : undefined
const isEdit = !!editId

// --- option lists ---
const statusOptions = [
  { label: 'Active', value: 'active' },
  { label: 'Inactive', value: 'inactive' },
  { label: 'Completed', value: 'completed' },
  { label: 'Cancelled', value: 'cancelled' },
]
// Currencies come from the global ISO 4217 endpoint; buildCurrencyOptions pins
// GBP/EUR/USD on top with a disabled dashed separator (shared with the expense form).
const currencies = ref<Currency[]>([])
const currencyOptions = computed(() => buildCurrencyOptions(currencies.value))

async function loadCurrencies() {
  try {
    currencies.value = await listCurrencies()
  } catch {
    // Non-fatal: leave the picker empty; the GBP default still submits.
  }
}
// The budget unit IS the API's budget_type. A zero/blank amount means "no budget"
// (we omit budget_type entirely), so the unit only matters once an amount is typed.
const budgetUnitOptions = [
  { label: 'Hours', value: 'hours' },
  { label: 'Days', value: 'days' },
  { label: 'Money', value: 'money' },
]
const billingRateUnitOptions = [
  { label: 'per hour', value: 'per_hour' },
  { label: 'per day', value: 'per_day' },
]

// --- contacts for the Contact dropdown (the list API gives us ids only) ---
const contactOptions = ref<{ label: string; value: string }[]>([])
const contactsError = ref('')

// Same name precedence as the list view: company → person → email.
function contactDisplayName(c: Contact): string {
  const org = c.organisation_name?.trim()
  if (org) return org
  const person = `${c.first_name ?? ''} ${c.last_name ?? ''}`.trim()
  if (person) return person
  return c.email?.trim() || '(no name)'
}

async function loadContacts() {
  contactsError.value = ''
  try {
    const list = await listContacts()
    contactOptions.value = list.map((c) => ({ label: contactDisplayName(c), value: c.id }))
  } catch (err) {
    contactsError.value = (err as ApiError)?.message ?? 'Could not load contacts.'
  }
}

// --- form state (seeded with the backend's / mockup's defaults) ---
const defaults = () => ({
  contactId: '',
  name: '',
  status: 'active',
  contractPoNumber: '',
  projectInvoiceSequence: false,
  currency: 'GBP',
  budgetAmount: '0', // matches the mockup's "0"; zero ⇒ no budget
  budgetUnit: 'hours',
  hoursPerDay: '8:00',
  billingRate: '0.0',
  billingRateUnit: 'per_hour',
  billingRatePlusVat: true, // schema default; surfaced as the "plus VAT" checkbox
  isIr35: false,
  startDate: null as Date | null,
  endDate: null as Date | null,
  includeUnbillableTime: true,
})
const form = reactive(defaults())

// "More options" starts expanded to match the mockup.
const showMore = ref(true)

// --- edit-mode load state ---
const loadingProject = ref(isEdit)
const loadError = ref('')

// API "YYYY-MM-DD" → local-midnight Date for the picker (no TZ day-shift).
function parseISODate(s: string | null | undefined): Date | null {
  if (!s) return null
  const d = new Date(`${s}T00:00:00`)
  return Number.isNaN(d.getTime()) ? null : d
}

async function loadForEdit() {
  if (!editId) return
  loadingProject.value = true
  loadError.value = ''
  try {
    const p = await getProject(editId)
    form.contactId = p.contact_id
    form.name = p.name
    form.status = p.status || 'active'
    form.contractPoNumber = p.contract_po_number ?? ''
    form.projectInvoiceSequence = p.project_invoice_sequence
    form.currency = p.currency || 'GBP'
    // Budget: show the amount for whichever single type is set; else default to 0 Hours.
    if (p.budget_type === 'hours') {
      form.budgetUnit = 'hours'
      form.budgetAmount = p.budget_hours ?? '0'
    } else if (p.budget_type === 'days') {
      form.budgetUnit = 'days'
      form.budgetAmount = p.budget_days != null ? String(p.budget_days) : '0'
    } else if (p.budget_type === 'money') {
      form.budgetUnit = 'money'
      form.budgetAmount = p.budget_money ?? '0'
    } else {
      form.budgetUnit = 'hours'
      form.budgetAmount = '0'
    }
    form.hoursPerDay = p.hours_per_day ?? '8:00'
    form.billingRate = p.billing_rate ?? '0.0'
    form.billingRateUnit = p.billing_rate_unit ?? 'per_hour'
    form.billingRatePlusVat = p.billing_rate_plus_vat
    form.isIr35 = p.is_ir35
    form.startDate = parseISODate(p.start_date)
    form.endDate = parseISODate(p.end_date)
    form.includeUnbillableTime = p.include_unbillable_time
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this project.'
  } finally {
    loadingProject.value = false
  }
}

onMounted(() => {
  loadContacts()
  loadCurrencies()
  if (isEdit) loadForEdit()
})

// --- validation (the two required fields; the backend is the final authority on
// formats and returns a 422 we surface in the banner) ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.contactId) errors.contactId = 'Choose a contact for this project.'
  if (form.name.trim() === '') errors.name = 'Enter a project name.'
  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')

// "0", "0.0", "0.00", "0:00" all count as "no budget".
function isZeroish(s: string): boolean {
  const t = s.trim()
  return t === '' || /^0+([.:]0+)?$/.test(t)
}

function buildPayload(): CreateProjectRequest {
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  const payload: CreateProjectRequest = {
    contact_id: form.contactId,
    name: form.name.trim(),
    status: form.status,
    contract_po_number: opt(form.contractPoNumber),
    project_invoice_sequence: form.projectInvoiceSequence,
    currency: form.currency,
    hours_per_day: opt(form.hoursPerDay),
    billing_rate: form.billingRate.trim() || '0',
    billing_rate_unit: form.billingRateUnit,
    billing_rate_plus_vat: form.billingRatePlusVat,
    is_ir35: form.isIr35,
    include_unbillable_time: form.includeUnbillableTime,
  }

  // Budget: only send when a non-zero amount is entered; the unit selects the field.
  if (!isZeroish(form.budgetAmount)) {
    const amount = form.budgetAmount.trim()
    payload.budget_type = form.budgetUnit
    if (form.budgetUnit === 'hours') payload.budget_hours = amount
    else if (form.budgetUnit === 'days') payload.budget_days = Number(amount)
    else if (form.budgetUnit === 'money') payload.budget_money = amount
  }

  // Dates: send "YYYY-MM-DD" only when picked.
  if (form.startDate) payload.start_date = toISODate(form.startDate)
  if (form.endDate) payload.end_date = toISODate(form.endDate)

  return payload
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    if (editId) {
      await updateProject(editId, buildPayload())
    } else {
      await createProject(buildPayload())
    }
    router.push('/projects')
  } catch (err) {
    // 401 is already handled by apiFetch. 400/403/404/422 land here.
    formError.value =
      (err as ApiError)?.message ??
      (editId
        ? 'Could not save your changes. Please try again.'
        : 'Could not create the project. Please try again.')
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push('/projects')
}

// Per the approved plan: a plain link to the contact form. Any unsaved input on
// this form is lost on navigation (no draft persistence yet — see BACKLOG).
function addNewContact() {
  router.push('/contacts/new')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">{{ isEdit ? 'Edit Project' : 'New Project' }}</h1>

    <!-- Edit: loading -->
    <FaCard v-if="isEdit && loadingProject" title="Project">
      <div class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
    </FaCard>

    <!-- Edit: load error -->
    <FaCard v-else-if="isEdit && loadError" title="Project">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Back to projects" severity="secondary" outlined @click="router.push('/projects')" />
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

      <!-- 1. Project -->
      <FaCard title="Project" note="Required fields *">
        <FormRow label="Contact" label-for="contact" required>
          <div class="flex flex-wrap items-center gap-3">
            <Select
              id="contact"
              v-model="form.contactId"
              :options="contactOptions"
              option-label="label"
              option-value="value"
              filter
              filter-placeholder="Search contacts"
              placeholder="Select a contact"
              class="w-72"
              :invalid="!!errors.contactId"
            />
            <button
              type="button"
              class="text-sm font-semibold text-fa-blue hover:underline"
              @click="addNewContact"
            >
              Add a new contact
            </button>
          </div>
          <p v-if="contactsError" class="text-xs text-[#c0392b]">{{ contactsError }}</p>
          <p v-if="errors.contactId" class="text-xs text-[#c0392b]">{{ errors.contactId }}</p>
        </FormRow>

        <FormRow label="Project Name" label-for="name" required>
          <InputText id="name" v-model="form.name" class="w-full max-w-md" :invalid="!!errors.name" />
          <p v-if="errors.name" class="text-xs text-[#c0392b]">{{ errors.name }}</p>
        </FormRow>

        <FormRow label="Status" label-for="status">
          <Select
            id="status"
            v-model="form.status"
            :options="statusOptions"
            option-label="label"
            option-value="value"
            class="w-56"
          />
        </FormRow>

        <FormRow label="Contract/PO Number" label-for="po">
          <InputText id="po" v-model="form.contractPoNumber" class="w-56" />
        </FormRow>

        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.projectInvoiceSequence" binary input-id="invoice-seq" />
            <span>Project-level Invoice Sequence?</span>
          </label>
        </FormRow>
      </FaCard>

      <!-- 2. Time and money -->
      <FaCard title="Time and money">
        <FormRow label="Currency" label-for="currency">
          <Select
            id="currency"
            v-model="form.currency"
            :options="currencyOptions"
            option-label="label"
            option-value="value"
            option-disabled="disabled"
            class="w-72"
          />
        </FormRow>

        <FormRow label="Budget" label-for="budget">
          <div class="flex items-center gap-2">
            <InputText id="budget" v-model="form.budgetAmount" inputmode="decimal" class="w-32" />
            <Select
              v-model="form.budgetUnit"
              :options="budgetUnitOptions"
              option-label="label"
              option-value="value"
              class="w-36"
              aria-label="Budget unit"
            />
          </div>
          <p class="text-xs text-fa-muted">Leave as zero if this project doesn't have a budget.</p>
        </FormRow>

        <FormRow label="Hours per day" label-for="hours-per-day">
          <InputText id="hours-per-day" v-model="form.hoursPerDay" class="w-32" />
          <p class="text-xs text-fa-muted">(e.g. 1:30 or 1.5)</p>
        </FormRow>

        <FormRow label="Normal Billing Rate" label-for="billing-rate">
          <div class="flex flex-wrap items-center gap-2">
            <InputGroup class="w-40">
              <InputGroupAddon>£</InputGroupAddon>
              <InputText id="billing-rate" v-model="form.billingRate" inputmode="decimal" />
            </InputGroup>
            <Select
              v-model="form.billingRateUnit"
              :options="billingRateUnitOptions"
              option-label="label"
              option-value="value"
              class="w-36"
              aria-label="Billing rate unit"
            />
            <label class="inline-flex items-center gap-2 text-sm text-fa-text">
              <Checkbox v-model="form.billingRatePlusVat" binary input-id="plus-vat" />
              <span>plus VAT</span>
            </label>
          </div>
        </FormRow>
      </FaCard>

      <!-- 3. More options (collapsible, expanded by default to match the mockup) -->
      <section class="mb-5 overflow-hidden rounded-[5px] border border-fa-border bg-white">
        <button
          type="button"
          class="flex w-full items-center gap-2 border-b border-fa-border bg-fa-card-header px-5 py-3 text-left"
          @click="showMore = !showMore"
        >
          <h2 class="text-[15px] font-bold text-fa-text">More options</h2>
          <i class="pi text-fa-muted" :class="showMore ? 'pi-angle-up' : 'pi-angle-down'" />
        </button>
        <div v-if="showMore" class="px-5 py-[22px]">
          <FormRow>
            <label class="inline-flex items-center gap-2 text-sm text-fa-text">
              <Checkbox v-model="form.isIr35" binary input-id="ir35" />
              <span>Is 'Employment' under IR35?</span>
            </label>
          </FormRow>

          <FormRow label="Starting Date" label-for="start-date">
            <DatePicker
              id="start-date"
              v-model="form.startDate"
              date-format="dd M yy"
              show-icon
              :show-on-focus="false"
              placeholder="dd mmm yy"
            />
            <p class="text-xs text-fa-muted">Leave blank if this project doesn't have a starting date.</p>
          </FormRow>

          <FormRow label="Ending Date" label-for="end-date">
            <DatePicker
              id="end-date"
              v-model="form.endDate"
              date-format="dd M yy"
              show-icon
              :show-on-focus="false"
              placeholder="dd mmm yy"
            />
            <p class="text-xs text-fa-muted">Leave blank if this project doesn't have an ending date.</p>
          </FormRow>

          <FormRow>
            <label class="inline-flex items-center gap-2 text-sm text-fa-text">
              <Checkbox v-model="form.includeUnbillableTime" binary input-id="unbillable" />
              <span>Include unbillable time</span>
            </label>
            <p class="text-xs text-fa-muted">Show unbillable time on your Project Profitability breakdown.</p>
          </FormRow>
        </div>
      </section>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button
          :label="isEdit ? 'Save changes' : 'Create new project'"
          :loading="submitting"
          @click="submit"
        />
        <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
