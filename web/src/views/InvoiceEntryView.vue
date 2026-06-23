<script setup lang="ts">
// Invoice HEADER form — DUAL-MODE:
//   /invoices/new       → create (POST /api/v1/invoices), no line items
//   /invoices/:id/edit  → edit   (PUT  /api/v1/invoices/:id), pre-filled
// Modelled on ProjectEntryView; the two cards mirror the FreeAgent "New Invoice"
// screen (Contact and project / Details). Line items are added/edited on the
// invoice DETAIL view, not here.
//
// FreeAgent collects "Payment terms (days)"; the backend stores a due_on DATE, so
// we convert terms → due_on = invoice date + N days on submit (and back on edit).
//
// IMPORTANT: the backend PUT REBUILDS all line items from the payload. So on EDIT
// we re-send the invoice's existing items unchanged — otherwise the save would wipe
// them.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import DatePicker from 'primevue/datepicker'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { getInvoice, createInvoice, updateInvoice } from '@/services/invoices.service'
import { listContacts } from '@/services/contacts.service'
import { listCurrencies } from '@/services/currencies.service'
import { buildCurrencyOptions } from '@/lib/currency'
import { toISODate } from '@/lib/format'
import type { Contact } from '@/types/contact'
import type { Currency } from '@/types/currency'
import type { CreateInvoiceRequest, InvoiceItem, InvoiceItemRequest } from '@/types/invoice'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const route = useRoute()

// Edit mode iff we're on /invoices/:id/edit (the create route has no :id param).
const editId = typeof route.params.id === 'string' ? route.params.id : undefined
const isEdit = !!editId

// --- contacts for the Contact dropdown (the list API gives us ids only) ---
const contactOptions = ref<{ label: string; value: string }[]>([])
const contactsError = ref('')

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

// --- currencies ---
const currencies = ref<Currency[]>([])
const currencyOptions = computed(() => buildCurrencyOptions(currencies.value))
async function loadCurrencies() {
  try {
    currencies.value = await listCurrencies()
  } catch {
    // Non-fatal: leave the picker empty; the GBP default still submits.
  }
}

// --- form state ---
const defaults = () => ({
  contactId: '',
  reference: '',
  invoiceDate: new Date() as Date | null,
  paymentTerms: '30', // days; 0 ⇒ "Due on Receipt"
  currency: 'GBP',
})
const form = reactive(defaults())

// The existing line items, kept verbatim on EDIT so the PUT (which rebuilds lines)
// doesn't wipe them. Empty on create.
const existingItems = ref<InvoiceItem[]>([])

// Whether the loaded invoice is still editable (DRAFT). Non-DRAFT invoices 409 on
// PUT, so we warn rather than letting the save look broken.
const loadedStatus = ref<string>('')
const notEditable = computed(() => isEdit && loadedStatus.value !== '' && loadedStatus.value !== 'DRAFT')

// --- edit-mode load state ---
const loadingInvoice = ref(isEdit)
const loadError = ref('')

function parseISODate(s: string | null | undefined): Date | null {
  if (!s) return null
  const d = new Date(`${s}T00:00:00`)
  return Number.isNaN(d.getTime()) ? null : d
}

// Whole days between two YYYY-MM-DD dates (due − dated), for the terms field.
function daysBetween(datedOn: string, dueOn: string): number {
  const a = parseISODate(datedOn)
  const b = parseISODate(dueOn)
  if (!a || !b) return 30
  return Math.round((b.getTime() - a.getTime()) / 86_400_000)
}

async function loadForEdit() {
  if (!editId) return
  loadingInvoice.value = true
  loadError.value = ''
  try {
    const inv = await getInvoice(editId)
    loadedStatus.value = inv.status
    form.contactId = inv.contact_id
    form.reference = inv.reference ?? ''
    form.invoiceDate = parseISODate(inv.dated_on)
    form.paymentTerms = inv.due_on ? String(daysBetween(inv.dated_on, inv.due_on)) : '30'
    form.currency = inv.currency || 'GBP'
    existingItems.value = inv.items ?? []
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this invoice.'
  } finally {
    loadingInvoice.value = false
  }
}

onMounted(() => {
  loadContacts()
  loadCurrencies()
  if (isEdit) loadForEdit()
})

// --- validation ---
const errors = reactive<Record<string, string>>({})
function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (!form.contactId) errors.contactId = 'Choose a contact for this invoice.'
  if (!form.invoiceDate) errors.invoiceDate = 'Pick an invoice date.'
  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')

function addDays(d: Date, n: number): Date {
  const r = new Date(d)
  r.setDate(r.getDate() + n)
  return r
}

// Map stored line items back to the request shape (the API returns the exact
// fields the request needs — price/quantity/sales_tax_rate round-trip).
function toItemRequests(items: InvoiceItem[]): InvoiceItemRequest[] {
  return items.map((it) => ({
    description: it.description,
    quantity: it.quantity,
    price: it.price,
    sales_tax_rate: it.sales_tax_rate,
  }))
}

function buildPayload(): CreateInvoiceRequest {
  const datedOn = toISODate(form.invoiceDate as Date)
  const terms = Number(form.paymentTerms)
  const dueOn = toISODate(addDays(form.invoiceDate as Date, Number.isFinite(terms) ? terms : 0))
  const reference = form.reference.trim()
  return {
    contact_id: form.contactId,
    dated_on: datedOn,
    due_on: dueOn,
    reference: reference === '' ? undefined : reference,
    currency: form.currency,
    // Create: no lines yet (added on the detail view). Edit: preserve existing.
    items: isEdit ? toItemRequests(existingItems.value) : [],
  }
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    if (editId) {
      await updateInvoice(editId, buildPayload())
      router.push(`/invoices/${editId}`)
    } else {
      const created = await createInvoice(buildPayload())
      // Header-first flow: land on the detail view to add line items.
      router.push(`/invoices/${created.id}`)
    }
  } catch (err) {
    formError.value =
      (err as ApiError)?.message ??
      (editId ? 'Could not save your changes.' : 'Could not create the invoice.')
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push(editId ? `/invoices/${editId}` : '/invoices')
}

function addNewContact() {
  router.push('/contacts/new')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">{{ isEdit ? 'Edit Invoice' : 'New Invoice' }}</h1>

    <!-- Edit: loading -->
    <FaCard v-if="isEdit && loadingInvoice" title="Invoice">
      <div class="py-10 text-center text-fa-muted"><i class="pi pi-spin pi-spinner mr-2" />Loading…</div>
    </FaCard>

    <!-- Edit: load error -->
    <FaCard v-else-if="isEdit && loadError" title="Invoice">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Back to invoices" severity="secondary" outlined @click="router.push('/invoices')" />
      </div>
    </FaCard>

    <template v-else>
      <div
        v-if="formError"
        class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
        role="alert"
      >
        {{ formError }}
      </div>

      <!-- A non-DRAFT invoice can't be edited (the backend 409s). Warn up-front. -->
      <div
        v-if="notEditable"
        class="mb-4 rounded border border-[#f0e0b6] bg-[#fdf6e3] px-3 py-2 text-sm text-[#8a6d3b]"
        role="status"
      >
        This invoice has been sent, so it can no longer be edited. Reopen it to draft first.
      </div>

      <!-- 1. Contact and project -->
      <FaCard title="Contact and project" note="Required fields *">
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
            <button type="button" class="text-sm font-semibold text-fa-blue hover:underline" @click="addNewContact">
              Add a new contact
            </button>
          </div>
          <p v-if="contactsError" class="text-xs text-[#c0392b]">{{ contactsError }}</p>
          <p v-if="errors.contactId" class="text-xs text-[#c0392b]">{{ errors.contactId }}</p>
        </FormRow>
      </FaCard>

      <!-- 2. Details -->
      <FaCard title="Details" note="Required fields *">
        <FormRow label="Invoice reference" label-for="reference">
          <InputText id="reference" v-model="form.reference" class="w-56" placeholder="e.g. 001" />
        </FormRow>

        <FormRow label="Invoice date" label-for="invoice-date" required>
          <DatePicker
            id="invoice-date"
            v-model="form.invoiceDate"
            date-format="dd M yy"
            show-icon
            :show-on-focus="false"
            placeholder="dd mmm yy"
            :invalid="!!errors.invoiceDate"
          />
          <p v-if="errors.invoiceDate" class="text-xs text-[#c0392b]">{{ errors.invoiceDate }}</p>
        </FormRow>

        <FormRow label="Payment terms" label-for="payment-terms">
          <InputGroup class="w-40">
            <InputText id="payment-terms" v-model="form.paymentTerms" inputmode="numeric" />
            <InputGroupAddon>days</InputGroupAddon>
          </InputGroup>
          <p class="text-xs text-fa-muted">Set to zero to display 'Due on Receipt' on the invoice.</p>
        </FormRow>

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
      </FaCard>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button
          :label="isEdit ? 'Save changes' : 'Create invoice'"
          :loading="submitting"
          @click="submit"
        />
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="cancel">Cancel</button>
      </div>
    </template>
  </AppLayout>
</template>
