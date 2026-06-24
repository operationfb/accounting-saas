<script setup lang="ts">
// Single invoice — the document-style "detail" view, wired to GET
// /api/v1/invoices/:id (header + line items). Modelled on the FreeAgent invoice
// screen: a Draft → Sent → Paid tracker, the invoice document (our org "from"
// block, the contact, the line-item table + totals), and the "New invoice item"
// modal for adding/editing lines.
//
// Line items live ON this view. Because the backend PUT REBUILDS all lines from
// the payload, every add/edit/delete sends the FULL current items array (built
// from the loaded invoice) via updateInvoice, then we use its response to refresh.
// All line-item editing + the header Edit/Delete are DRAFT-only (the backend 409s
// otherwise), so those controls hide once the invoice is sent.
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Dialog from 'primevue/dialog'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import InvoiceItemDialog from '@/components/InvoiceItemDialog.vue'
import {
  getInvoice,
  updateInvoice,
  deleteInvoice,
  changeInvoiceStatus,
} from '@/services/invoices.service'
import { getContact } from '@/services/contacts.service'
import { getOrganisation } from '@/services/organisation.service'
import { listVatRates } from '@/services/expenses.service'
import { formatMoney, formatDate } from '@/lib/format'
import { invoiceStatusClass, invoiceStep } from '@/lib/invoiceStatus'
import type { Invoice, InvoiceItem, InvoiceItemRequest, CreateInvoiceRequest } from '@/types/invoice'
import type { Contact } from '@/types/contact'
import type { OrganisationDetails } from '@/types/organisation'
import type { VatRate } from '@/types/expense'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const id = route.params.id as string

const invoice = ref<Invoice | null>(null)
const org = ref<OrganisationDetails | null>(null)
const contact = ref<Contact | null>(null)
const vatRates = ref<VatRate[]>([])
const loading = ref(true)
const error = ref<ApiError | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    invoice.value = await getInvoice(id)
    // The org "from" block + the contact "to" block are best-effort: a failure
    // there shouldn't blank the whole page (the invoice itself carries its 404/403).
    const [orgRes, contactRes] = await Promise.allSettled([
      getOrganisation(),
      getContact(invoice.value.contact_id),
    ])
    org.value = orgRes.status === 'fulfilled' ? orgRes.value : null
    contact.value = contactRes.status === 'fulfilled' ? contactRes.value : null
  } catch (err) {
    error.value = err as ApiError
    invoice.value = null
  } finally {
    loading.value = false
  }
}

async function loadVatRates() {
  try {
    vatRates.value = await listVatRates()
  } catch {
    // Non-fatal: the modal's VAT picker just falls back to a 0% default.
  }
}

onMounted(() => {
  load()
  loadVatRates()
})

const notFound = computed(() => error.value?.status === 404)
const isDraft = computed(() => invoice.value?.status === 'DRAFT')
const items = computed<InvoiceItem[]>(() => invoice.value?.items ?? [])
const currency = computed(() => invoice.value?.currency ?? 'GBP')
const currencySymbol = computed(() => (currency.value === 'GBP' ? '£' : currency.value))

// VAT picker options for the modal: value is the percentage string the API wants
// (rate_bps/100, e.g. 2000 → "20"), which round-trips with the line's stored rate.
// Manual (is_fixed_ratio=false) rates are excluded — invoices use fixed add-on VAT only.
const vatOptions = computed(() =>
  vatRates.value
    .filter((r) => r.is_fixed_ratio)
    .map((r) => ({ label: `${r.name} (${r.rate})`, value: String(r.rate_bps / 100) })),
)

// --- Draft → Sent → Paid tracker ---
const steps = [
  { key: 'draft', label: 'Draft' },
  { key: 'sent', label: 'Sent' },
  { key: 'paid', label: 'Paid' },
] as const
const stepOrder: Record<string, number> = { draft: 0, sent: 1, paid: 2 }
// Which tracker node the invoice sits at. A fully-paid invoice already maps to 'paid'
// (via its display_status); a SENT invoice with a PARTIAL payment is promoted to 'paid'
// too (shown as "Part paid" below) so the bar reflects money received rather than
// sitting on "Sent". WRITTEN_OFF/REFUNDED stay 'other' (tracker hidden).
const activeStep = computed(() => {
  if (!invoice.value) return 'draft'
  const base = invoiceStep(invoice.value.status, invoice.value.display_status)
  if (base === 'sent' && hasPayments.value) return 'paid'
  return base
})
// -1 for WRITTEN_OFF/REFUNDED ('other'): the tracker isn't shown for those.
const activeIndex = computed(() => (activeStep.value === 'other' ? -1 : stepOrder[activeStep.value]))
function isStepActive(key: string): boolean {
  return activeIndex.value >= 0 && activeIndex.value === stepOrder[key]
}
function isStepDone(key: string): boolean {
  return activeIndex.value >= 0 && activeIndex.value > stepOrder[key]
}
// A SENT invoice with money recorded against it (a bank Invoice Receipt) is a live,
// part-paid receivable — the backend refuses to reopen it (409), so don't offer the
// action. The receipt(s) must be removed first (in the banking explain panel).
const hasPayments = computed(() => Number(invoice.value?.paid_value ?? '0') > 0)
const canReopen = computed(() => invoice.value?.status === 'SENT' && !hasPayments.value)
// Tracker's final node: fully settled (display_status Paid/Overpaid) reads "Paid";
// some-but-not-all paid reads "Part paid".
const fullyPaid = computed(
  () => invoice.value?.display_status === 'Paid' || invoice.value?.display_status === 'Overpaid',
)
const partiallyPaid = computed(() => hasPayments.value && !fullyPaid.value)
// Returns the status action to run when a tracker step is clicked, or null if it's not interactive.
// Draft state → "Sent" step is clickable (issue); Sent (unpaid) state → "Draft" step is clickable (reopen).
function stepAction(key: string): 'issue' | 'reopen' | null {
  if (key === 'sent' && isDraft.value) return 'issue'
  if (key === 'draft' && canReopen.value) return 'reopen'
  return null
}
// Dynamic label: interactive steps get an action label; others keep their static label.
function stepLabel(s: { key: string; label: string }): string {
  if (s.key === 'sent' && isDraft.value) return 'Mark as Sent'
  if (s.key === 'draft' && canReopen.value) return 'Make Draft'
  if (s.key === 'paid' && partiallyPaid.value) return 'Part paid'
  return s.label
}

// --- contact / org presentation ---
function contactName(c: Contact | null): string {
  if (!c) return '—'
  const org = c.organisation_name?.trim()
  if (org) return org
  const person = `${c.first_name ?? ''} ${c.last_name ?? ''}`.trim()
  if (person) return person
  return c.email?.trim() || '—'
}
function contactLines(c: Contact | null): string[] {
  if (!c) return []
  return [c.address_line_1, c.address_line_2, c.address_line_3, c.town, c.postcode]
    .map((s) => s?.trim())
    .filter((s): s is string => !!s)
}
function orgLines(o: OrganisationDetails | null): string[] {
  if (!o) return []
  return [o.address_line_1, o.address_line_2, o.address_line_3, o.town, o.postcode]
    .map((s) => s?.trim())
    .filter((s): s is string => !!s)
}

// --- line-item editing (DRAFT only) ---
const dialogVisible = ref(false)
const editingItem = ref<InvoiceItemRequest | null>(null)
const editingIndex = ref<number | null>(null)
const savingItems = ref(false)

// The loaded invoice's lines mapped back to the request shape — the basis every
// mutation sends (the PUT rebuilds from this full list).
function currentItemRequests(): InvoiceItemRequest[] {
  return items.value.map((it) => ({
    description: it.description,
    quantity: it.quantity,
    price: it.price,
    sales_tax_rate: it.sales_tax_rate,
  }))
}

function headerPayload(itemReqs: InvoiceItemRequest[]): CreateInvoiceRequest {
  const inv = invoice.value!
  return {
    contact_id: inv.contact_id,
    dated_on: inv.dated_on,
    due_on: inv.due_on ?? undefined,
    reference: inv.reference ?? '',
    currency: inv.currency,
    items: itemReqs,
  }
}

const actionError = ref('')
const actionSuccess = ref('')

async function persistItems(itemReqs: InvoiceItemRequest[]) {
  savingItems.value = true
  actionError.value = ''
  actionSuccess.value = ''
  try {
    // Use the PUT response (recomputed totals + lines) to refresh in place.
    invoice.value = await updateInvoice(id, headerPayload(itemReqs))
    return true
  } catch (err) {
    actionError.value = (err as ApiError)?.message ?? 'Could not save the invoice item.'
    return false
  } finally {
    savingItems.value = false
  }
}

function openAddItem() {
  editingItem.value = null
  editingIndex.value = null
  dialogVisible.value = true
}
function openEditItem(index: number) {
  const it = items.value[index]
  editingItem.value = {
    description: it.description,
    quantity: it.quantity,
    price: it.price,
    sales_tax_rate: it.sales_tax_rate,
  }
  editingIndex.value = index
  dialogVisible.value = true
}

async function onItemSave(payload: { item: InvoiceItemRequest; addAnother: boolean }) {
  const reqs = currentItemRequests()
  if (editingIndex.value === null) reqs.push(payload.item)
  else reqs[editingIndex.value] = payload.item
  const ok = await persistItems(reqs)
  // "Add another" keeps the dialog open (and in append mode); finish/edit closes it.
  if (ok && !payload.addAnother) dialogVisible.value = false
}

async function deleteItem(index: number) {
  const reqs = currentItemRequests()
  reqs.splice(index, 1)
  await persistItems(reqs)
}

// --- status actions (issue / reopen) ---
const acting = ref(false)
async function runStatus(action: 'issue' | 'reopen') {
  if (acting.value) return
  acting.value = true
  actionError.value = ''
  actionSuccess.value = ''
  try {
    invoice.value = await changeInvoiceStatus(id, action)
    actionSuccess.value = action === 'issue' ? 'Invoice marked as sent.' : 'Invoice reopened — it’s a draft again.'
  } catch (err) {
    actionError.value = (err as ApiError)?.message ?? 'Could not update the invoice status.'
  } finally {
    acting.value = false
  }
}

// --- delete (DRAFT only) ---
const deleteDialog = ref(false)
const deleting = ref(false)
async function confirmDelete() {
  deleting.value = true
  actionError.value = ''
  try {
    await deleteInvoice(id)
    router.push('/invoices')
  } catch (err) {
    actionError.value = (err as ApiError)?.message ?? 'Could not delete the invoice.'
    deleteDialog.value = false
    deleting.value = false
  }
}

function backToList() {
  router.push('/invoices')
}
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-3">
        <h1 class="text-[22px] font-bold">Invoice</h1>
        <span
          v-if="invoice"
          class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
          :class="invoiceStatusClass(invoice.display_status)"
          >{{ invoice.display_status }}</span
        >
      </div>
      <div class="flex flex-wrap gap-2.5">
        <Button
          v-if="isDraft"
          label="Edit"
          icon="pi pi-pencil"
          severity="secondary"
          @click="router.push(`/invoices/${id}/edit`)"
        />
        <Button
          v-if="isDraft"
          label="Delete"
          icon="pi pi-trash"
          severity="danger"
          outlined
          @click="deleteDialog = true"
        />
        <Button label="Back to list" severity="secondary" outlined @click="backToList" />
      </div>
    </div>

    <!-- Status-action feedback. -->
    <div
      v-if="actionError"
      class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
      role="alert"
    >
      {{ actionError }}
    </div>
    <div
      v-if="actionSuccess"
      class="mb-4 rounded border border-[#cfe9c7] bg-[#eaf7e6] px-3 py-2 text-sm text-[#3f8038]"
      role="status"
    >
      {{ actionSuccess }}
    </div>

    <!-- Draft → Sent → Paid tracker (hidden for written-off / refunded). -->
    <div
      v-if="invoice && activeStep !== 'other'"
      class="mb-5 flex items-center gap-3 rounded-[5px] border border-fa-border bg-white px-5 py-3.5"
    >
      <template v-for="(s, i) in steps" :key="s.key">
        <!-- Render as a button when this step has an action, plain div otherwise. -->
        <component
          :is="stepAction(s.key) ? 'button' : 'div'"
          type="button"
          class="flex items-center gap-2"
          :class="
            stepAction(s.key)
              ? '-mx-1.5 -my-0.5 cursor-pointer rounded px-1.5 py-0.5 transition-colors hover:bg-[#f0f7ff] disabled:opacity-50'
              : ''
          "
          :disabled="stepAction(s.key) ? acting : undefined"
          @click="() => { const a = stepAction(s.key); if (a) runStatus(a) }"
        >
          <span
            class="inline-flex h-5 w-5 items-center justify-center rounded-full border-2"
            :class="
              isStepActive(s.key) || isStepDone(s.key)
                ? 'border-fa-green text-fa-green'
                : stepAction(s.key)
                  ? 'border-fa-blue text-fa-blue'
                  : 'border-[#c2c9d1] text-[#c2c9d1]'
            "
          >
            <i v-if="acting && stepAction(s.key)" class="pi pi-spin pi-spinner text-[10px]" />
            <i v-else-if="isStepDone(s.key)" class="pi pi-check text-[10px]" />
            <span
              v-else
              class="h-2 w-2 rounded-full"
              :class="isStepActive(s.key) ? 'bg-fa-green' : 'bg-transparent'"
            />
          </span>
          <span
            class="text-sm font-semibold"
            :class="
              isStepActive(s.key)
                ? 'text-fa-text'
                : stepAction(s.key)
                  ? 'text-fa-blue'
                  : 'text-fa-muted'
            "
            >{{ stepLabel(s) }}</span
          >
        </component>
        <i v-if="i < steps.length - 1" class="pi pi-angle-right text-[#c2c9d1]" />
      </template>
    </div>

    <!-- The invoice document. -->
    <FaCard>
      <!-- Loading -->
      <div v-if="loading" class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>

      <!-- Error: 404 / 403 / other -->
      <div v-else-if="error" class="py-10 text-center">
        <p class="mb-1 font-semibold">{{ notFound ? 'Invoice not found' : 'Could not load this invoice' }}</p>
        <p class="mb-4 text-sm text-fa-muted">{{ error.message }}</p>
        <Button label="Back to list" severity="secondary" outlined @click="backToList" />
      </div>

      <!-- Document -->
      <div v-else-if="invoice">
        <!-- From (org) + invoice meta -->
        <div class="flex flex-wrap justify-between gap-6">
          <!-- Contact (billed to) -->
          <div class="min-w-[200px]">
            <p class="font-semibold text-fa-text">{{ contactName(contact) }}</p>
            <p v-for="line in contactLines(contact)" :key="line" class="text-sm text-fa-muted">{{ line }}</p>
          </div>
          <!-- Our org -->
          <div class="min-w-[200px] text-right">
            <p class="font-semibold text-fa-text">{{ org?.name || '—' }}</p>
            <p v-for="line in orgLines(org)" :key="line" class="text-sm text-fa-muted">{{ line }}</p>
            <p v-if="org?.vrn" class="mt-1 text-sm text-fa-muted">VAT: {{ org.vrn }}</p>
          </div>
        </div>

        <div class="mt-6 text-right">
          <p class="text-lg font-bold text-fa-text">INVOICE {{ invoice.reference || '' }}</p>
          <p class="text-sm font-semibold text-fa-text">{{ formatDate(invoice.dated_on) }}</p>
          <p v-if="invoice.due_on" class="text-sm text-fa-muted">
            Payment due by {{ formatDate(invoice.due_on) }}
          </p>
        </div>

        <!-- Line items -->
        <table class="mt-5 w-full border-collapse text-sm">
          <thead>
            <tr class="bg-fa-text text-white">
              <th class="px-3 py-2 text-left font-semibold">Quantity</th>
              <th class="px-3 py-2 text-left font-semibold">Details</th>
              <th class="px-3 py-2 text-right font-semibold">Unit Price ({{ currency }})</th>
              <th class="px-3 py-2 text-right font-semibold">VAT</th>
              <th class="px-3 py-2 text-right font-semibold">Net Subtotal ({{ currency }})</th>
              <th v-if="isDraft" class="px-3 py-2" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="(it, i) in items" :key="it.id" class="border-b border-[#eef1f4]">
              <td class="px-3 py-2.5 align-top tabular-nums">{{ it.quantity }}</td>
              <td class="px-3 py-2.5 align-top">
                <button
                  v-if="isDraft"
                  type="button"
                  class="text-left text-fa-blue hover:underline"
                  @click="openEditItem(i)"
                >
                  {{ it.description }}
                </button>
                <span v-else>{{ it.description }}</span>
              </td>
              <td class="px-3 py-2.5 text-right align-top tabular-nums">
                {{ formatMoney(it.price, currency) }}
              </td>
              <td class="px-3 py-2.5 text-right align-top tabular-nums">{{ it.sales_tax_rate }}%</td>
              <td class="px-3 py-2.5 text-right align-top tabular-nums">
                {{ formatMoney(it.net_value, currency) }}
              </td>
              <td v-if="isDraft" class="px-3 py-2.5 text-right align-top">
                <button
                  type="button"
                  class="text-fa-muted hover:text-[#c0392b] disabled:opacity-50"
                  :disabled="savingItems"
                  aria-label="Remove line"
                  @click="deleteItem(i)"
                >
                  <i class="pi pi-times" />
                </button>
              </td>
            </tr>

            <!-- Totals -->
            <tr>
              <td :colspan="isDraft ? 4 : 3" />
              <td class="px-3 pt-3 text-right text-fa-muted">Net Total</td>
              <td class="px-3 pt-3 text-right tabular-nums">{{ formatMoney(invoice.net_value, currency) }}</td>
            </tr>
            <tr>
              <td :colspan="isDraft ? 4 : 3" />
              <td class="px-3 py-1 text-right text-fa-muted">VAT</td>
              <td class="px-3 py-1 text-right tabular-nums">{{ formatMoney(invoice.sales_tax_value, currency) }}</td>
            </tr>
            <tr class="border-t-2 border-fa-text">
              <td :colspan="isDraft ? 4 : 3" />
              <td class="px-3 py-2 text-right font-bold">{{ currency }} Total</td>
              <td class="px-3 py-2 text-right font-bold tabular-nums">
                {{ formatMoney(invoice.total_value, currency) }}
              </td>
            </tr>
          </tbody>
        </table>

        <div v-if="isDraft" class="mt-3">
          <Button label="Add invoice item" icon="pi pi-plus" severity="success" @click="openAddItem" />
        </div>

        <!-- Footer: payment details + other info -->
        <div class="mt-8 grid grid-cols-1 gap-6 border-t border-fa-border pt-5 sm:grid-cols-2">
          <div>
            <p class="font-bold text-fa-text">Payment Details</p>
            <p class="mt-1 text-sm text-fa-text">
              <span class="font-semibold">Payment Reference:</span> {{ invoice.reference || '—' }}
            </p>
          </div>
          <div>
            <p class="font-bold text-fa-text">Other Information</p>
            <p v-if="org?.companies_house_number" class="mt-1 text-sm text-fa-text">
              <span class="font-semibold">Company Registration Number:</span> {{ org.companies_house_number }}
            </p>
          </div>
        </div>
      </div>
    </FaCard>

    <!-- New / Edit invoice item modal. -->
    <InvoiceItemDialog
      v-model:visible="dialogVisible"
      :vat-options="vatOptions"
      :edit-item="editingItem"
      :currency-symbol="currencySymbol"
      :saving="savingItems"
      @save="onItemSave"
    />

    <!-- Delete confirm. -->
    <Dialog
      v-model:visible="deleteDialog"
      modal
      header="Delete invoice"
      :style="{ width: '28rem' }"
      :closable="!deleting"
    >
      <p class="text-sm text-fa-muted">Delete this draft invoice? This can't be undone.</p>
      <template #footer>
        <button
          type="button"
          class="mr-3 font-semibold text-fa-blue hover:underline disabled:opacity-50"
          :disabled="deleting"
          @click="deleteDialog = false"
        >
          Cancel
        </button>
        <Button label="Delete invoice" severity="danger" :loading="deleting" @click="confirmDelete" />
      </template>
    </Dialog>
  </AppLayout>
</template>
