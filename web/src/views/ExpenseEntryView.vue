<script setup lang="ts">
// Expense form — DUAL-MODE:
//   /expenses/new        → create  (POST /api/v1/expenses)
//   /expenses/:id/edit   → edit    (PUT  /api/v1/expenses/:id), pre-filled from the record
// Supported fields are live (incl. VAT rate + amount); the Attachment / Project /
// Recurring sections + the VAT-options radios stay disabled ("coming soon").
import { ref, reactive, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter, useRoute, onBeforeRouteLeave } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import DatePicker from 'primevue/datepicker'
import RadioButton from 'primevue/radiobutton'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import Dialog from 'primevue/dialog'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import AttachmentsField from '@/components/AttachmentsField.vue'
import {
  listCategories,
  listVatRates,
  createExpense,
  getExpense,
  updateExpense,
  captureExpense,
  deleteExpense,
} from '@/services/expenses.service'
import { toISODate, computeFixedVatPounds } from '@/lib/format'
import type { ExpenseCategory, VatRate, CreateExpenseRequest, ExpenseDetail } from '@/types/expense'
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

// --- Smart Upload (OCR) ---
// The skeleton description the backend stamps on a fresh capture; OcrService
// replaces it with a composed one when extraction yields a description. We blank
// it on load so the user never sees the placeholder in the field. Mirror of the
// backend's placeholderDescription (attachment_service.go).
const PLACEHOLDER_DESCRIPTION = 'Awaiting review'

// Accept list + size cap mirror AttachmentsField (kept local to avoid coupling the
// two components). The backend re-sniffs the real MIME type and re-checks the size
// and is the final authority — this is just early client-side UX.
const SMART_ACCEPT = '.pdf,.jpg,.jpeg,.png,application/pdf,image/jpeg,image/png'
const SMART_ACCEPTED_TYPES = ['image/jpeg', 'image/png', 'application/pdf']
const SMART_EXT_TO_TYPE: Record<string, string> = {
  pdf: 'application/pdf',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  png: 'image/png',
}
const smartMaxParsed = Number(import.meta.env.VITE_MAX_UPLOAD_MB)
const SMART_MAX_MB = Number.isFinite(smartMaxParsed) && smartMaxParsed > 0 ? smartMaxParsed : 20
const SMART_MAX_BYTES = SMART_MAX_MB * 1024 * 1024

// OCR polling cadence/timeout — from env with sane fallbacks (same idiom as the
// VAT cap above). INTERVAL is the gap between polls; TIMEOUT is when we give up
// and let the user fill in manually.
const intervalParsed = Number(import.meta.env.VITE_OCR_POLL_INTERVAL_MS)
const OCR_POLL_INTERVAL_MS = Number.isFinite(intervalParsed) && intervalParsed > 0 ? intervalParsed : 2500
const timeoutParsed = Number(import.meta.env.VITE_OCR_POLL_TIMEOUT_MS)
const OCR_POLL_TIMEOUT_MS = Number.isFinite(timeoutParsed) && timeoutParsed > 0 ? timeoutParsed : 45000

// Smart Upload dialog + capture state (create mode).
const smartDialog = ref(false)
const smartDocType = ref<'receipt' | 'invoice' | null>(null)
const smartFileInput = ref<HTMLInputElement | null>(null)
const capturing = ref(false)
const smartError = ref('')

// OCR review/polling state (edit mode, captured drafts).
const ocrPolling = ref(false)
const ocrFilename = ref('')
const ocrState = ref<'' | 'reading' | 'complete' | 'failed' | 'timeout'>('')
let ocrTimer: ReturnType<typeof setTimeout> | null = null

// Discard-on-cancel: a fresh capture redirects here with ?captured=1, marking the
// skeleton eligible to be deleted if the user abandons it (Cancel / navigate away).
const justCaptured = route.query.captured === '1'
const discardOnLeave = ref(justCaptured)

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

    // Smart Upload capture awaiting review? (needs_review marks a capture.) Drive
    // the OCR review UX: poll while extraction runs, or fill from the result if
    // it's already done. We stay inside the `hydrating` window so the VAT watcher
    // doesn't clear/recompute the values we set here.
    if (exp.needs_review) {
      const att = primaryAttachment(exp)
      ocrFilename.value = att?.file_name ?? ''
      const status = att?.ocr_status ?? null
      if (status === 'COMPLETE') {
        // Already extracted (e.g. reopened from the review inbox): fill from the
        // real values + pick the manual VAT rate, and show the review banner.
        fillFormFromOcr(exp)
        ocrState.value = 'complete'
      } else {
        // Blank the skeleton placeholders so the user never sees fake data. Leave
        // category as-is (Sundries is a real, intended default).
        if (form.description === PLACEHOLDER_DESCRIPTION) form.description = ''
        if (form.totalValue === '0.00') form.totalValue = ''
        if (form.vatAmount === '0.00') form.vatAmount = ''
        if (status === 'FAILED' || status === 'SKIPPED') ocrState.value = 'failed'
        else pollOcr(editId) // PENDING/PROCESSING (or not started) → poll
      }
    }

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

// Stop any in-flight OCR poll when leaving the page (avoids a timer firing on a
// torn-down component).
onUnmounted(stopOcrPolling)

// Discard an abandoned fresh capture. This fires for ANY navigation away (a nav
// link, the back button) other than Save/Cancel, which clear discardOnLeave first.
// Best-effort: on failure the draft just stays in the review inbox; never blocks nav.
onBeforeRouteLeave(async () => {
  if (discardOnLeave.value && editId) {
    discardOnLeave.value = false
    try {
      await deleteExpense(editId)
    } catch {
      // leave it in the inbox on failure
    }
  }
  return true
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
      // Saved → this is now a confirmed expense (the backend clears needs_review),
      // so the route-leave guard must NOT discard it if it was a Smart Upload capture.
      discardOnLeave.value = false
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

async function cancel() {
  // Abandoning a fresh capture → discard the skeleton (best-effort) so it doesn't
  // linger in the review inbox. discardOnLeave is cleared first so the route-leave
  // guard doesn't try to delete it a second time.
  if (discardOnLeave.value && editId) {
    discardOnLeave.value = false
    try {
      await deleteExpense(editId)
    } catch {
      // leave it in the inbox on failure
    }
    router.push('/expenses')
    return
  }
  router.push(editId ? `/expenses/${editId}` : '/expenses')
}

// =============================================================================
// Smart Upload (OCR)
// =============================================================================

// The primary attachment carries the OCR status we poll. Fall back to the first
// file if none is flagged primary (shouldn't happen on a capture skeleton).
function primaryAttachment(exp: ExpenseDetail) {
  const atts = exp.attachments ?? []
  return atts.find((a) => a.is_primary) ?? atts[0]
}

// Overwrite the form fields OCR fills, from a COMPLETE capture. Pure field-setting
// (no `hydrating` toggle) so callers control the VAT-watcher suppression window.
function fillFormFromOcr(exp: ExpenseDetail) {
  // Category: the suggested one when OCR matched the vendor, else the Sundries
  // placeholder — so Sundries stays selected when nothing was suggested.
  form.category = exp.category_id
  // Description: the composed value, blanking the leftover skeleton placeholder.
  form.description = exp.description === PLACEHOLDER_DESCRIPTION ? '' : exp.description
  form.datedOn = new Date(`${exp.dated_on}T00:00:00`)
  // Money is a pound string already; treat the 0 placeholder as "not found".
  form.totalValue = exp.gross_value === '0.00' ? '' : exp.gross_value
  form.vatAmount = exp.vat_value === '0.00' ? '' : exp.vat_value
  form.supplierName = exp.supplier_name ?? ''
  form.supplierVat = exp.supplier_vat_number ?? ''
  form.invoiceNumber = exp.invoice_number ?? ''
  // If OCR found a VAT amount, select the first CUSTOM (non-fixed-ratio) rate — the
  // "Standard Rate (manual)" — so the watcher KEEPS the extracted amount instead of
  // recomputing it from the total (a fixed-ratio rate would overwrite vat_value).
  if (Number(exp.vat_value) > 0) {
    const manual = vatRates.value.find((r) => !r.is_fixed_ratio)
    if (manual) form.vatRate = manual.id
  }
}

// Inject a COMPLETE result mid-poll. Wraps fillFormFromOcr in the `hydrating`
// window so the VAT watcher doesn't fire while we set vatRate + vatAmount together.
async function injectOcr(exp: ExpenseDetail) {
  hydrating.value = true
  fillFormFromOcr(exp)
  await nextTick()
  hydrating.value = false
}

function stopOcrPolling() {
  if (ocrTimer) {
    clearTimeout(ocrTimer)
    ocrTimer = null
  }
  ocrPolling.value = false
}

// Poll the expense until its primary attachment's OCR is terminal, then inject
// (COMPLETE) or surface a manual-entry note (FAILED/SKIPPED/timeout). The cadence
// and overall budget come from env (OCR_POLL_INTERVAL_MS / OCR_POLL_TIMEOUT_MS).
function pollOcr(id: string) {
  const maxTries = Math.ceil(OCR_POLL_TIMEOUT_MS / OCR_POLL_INTERVAL_MS)
  let tries = 0
  ocrPolling.value = true
  ocrState.value = 'reading'

  const tick = async () => {
    tries++
    try {
      const exp = await getExpense(id)
      const status = primaryAttachment(exp)?.ocr_status ?? null
      if (status === 'COMPLETE') {
        await injectOcr(exp)
        ocrState.value = 'complete'
        stopOcrPolling()
        return
      }
      if (status === 'FAILED' || status === 'SKIPPED') {
        ocrState.value = 'failed'
        stopOcrPolling()
        return
      }
    } catch {
      // Transient read error — keep trying until the timeout budget is spent.
    }
    if (tries >= maxTries) {
      ocrState.value = 'timeout'
      stopOcrPolling()
      return
    }
    ocrTimer = setTimeout(tick, OCR_POLL_INTERVAL_MS)
  }
  ocrTimer = setTimeout(tick, OCR_POLL_INTERVAL_MS)
}

function openSmartUpload() {
  smartError.value = ''
  smartDocType.value = null
  smartDialog.value = true
}

// Picking a document type opens the OS file picker; onSmartFilePicked runs next.
function chooseSmartType(type: 'receipt' | 'invoice') {
  smartDocType.value = type
  smartError.value = ''
  smartFileInput.value?.click()
}

// Local mirror of AttachmentsField's file validation. Returns '' when acceptable.
function smartValidate(file: File): string {
  let type = file.type
  if (!SMART_ACCEPTED_TYPES.includes(type)) {
    const ext = file.name.split('.').pop()?.toLowerCase() ?? ''
    type = SMART_EXT_TO_TYPE[ext] ?? type
  }
  if (!SMART_ACCEPTED_TYPES.includes(type)) return 'Unsupported file type — use a PDF, JPEG or PNG.'
  if (file.size <= 0) return 'That file is empty.'
  if (file.size > SMART_MAX_BYTES) return `That file is larger than ${SMART_MAX_MB} MB.`
  return ''
}

async function onSmartFilePicked(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  const docType = smartDocType.value
  input.value = '' // reset so picking the same file again still fires `change`
  if (!file || !docType) return

  const err = smartValidate(file)
  if (err) {
    smartError.value = err
    return
  }

  capturing.value = true
  smartError.value = ''
  try {
    const created = await captureExpense(file, docType)
    smartDialog.value = false
    // Capture created a real DRAFT; go edit it and poll for OCR. The `captured`
    // flag makes the draft discard-on-cancel eligible (see onBeforeRouteLeave).
    router.push({ name: 'expense-edit', params: { id: created.id }, query: { captured: '1' } })
  } catch (err) {
    smartError.value = (err as ApiError)?.message ?? 'Could not start Smart Upload. Please try again.'
  } finally {
    capturing.value = false
  }
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

      <!-- Smart Upload OCR review banners (edit mode, captured drafts). -->
      <div
        v-if="ocrState === 'reading'"
        class="mb-4 flex items-center gap-2 rounded border border-[#cfe0f3] bg-[#eef5fc] px-3 py-2 text-sm text-[#1f5fa6]"
        role="status"
      >
        <i class="pi pi-spin pi-spinner" />
        <span>Reading <strong>{{ ocrFilename }}</strong>… we’ll fill in the highlighted fields.</span>
      </div>
      <div
        v-else-if="ocrState === 'complete'"
        class="mb-4 rounded border border-[#cfe9c7] bg-[#eaf7e6] px-3 py-2 text-sm text-[#3f8038]"
        role="status"
      >
        Filled in from <strong>{{ ocrFilename }}</strong>. Please review the details and save.
      </div>
      <div
        v-else-if="ocrState === 'failed' || ocrState === 'timeout'"
        class="mb-4 rounded border border-[#f3e2c0] bg-[#fcf6e9] px-3 py-2 text-sm text-[#8a6d1f]"
        role="status"
      >
        <template v-if="ocrState === 'failed'">
          We couldn’t read <strong>{{ ocrFilename }}</strong> automatically — please enter the details below.
        </template>
        <template v-else>
          Still reading <strong>{{ ocrFilename }}</strong> — you can enter the details now, or reload in a moment.
        </template>
      </div>

      <!-- Receipts — manual "Add files" plus, in create mode, "Smart upload", which
           captures a receipt/invoice and lets OCR pre-fill the form. The chooser
           dialog + capture logic live in this view; only the buttons sit in the card. -->
      <AttachmentsField ref="attachments" :expense-id="editId">
        <template v-if="!isEdit" #files-actions>
          <Button
            type="button"
            label="Smart upload"
            icon="pi pi-sparkles"
            :loading="capturing"
            @click="openSmartUpload"
          />
        </template>
        <template v-if="!isEdit" #files-hint>
          Add receipts yourself, or let Smart upload read a receipt/invoice and fill
          in the details for you.
        </template>
      </AttachmentsField>

      <!-- Smart Upload chooser: pick the document type, then the OS file picker
           opens (onSmartFilePicked fires the capture). The hidden input lives
           outside the Dialog so its ref is always mounted. -->
      <Dialog
        v-model:visible="smartDialog"
        modal
        header="Smart upload"
        :style="{ width: '30rem' }"
        :closable="!capturing"
      >
        <p class="mb-4 text-sm text-fa-muted">What are you uploading? We’ll use the right reader.</p>
        <div class="flex flex-col gap-3">
          <button
            type="button"
            class="flex items-start gap-3 rounded border border-fa-border p-3 text-left hover:border-fa-blue disabled:opacity-50"
            :disabled="capturing"
            @click="chooseSmartType('receipt')"
          >
            <i class="pi pi-receipt mt-0.5 text-lg text-fa-blue" />
            <span>
              <span class="block font-semibold">Scanned receipt</span>
              <span class="block text-xs text-fa-muted">A photo or scan of a till/POS receipt.</span>
            </span>
          </button>
          <button
            type="button"
            class="flex items-start gap-3 rounded border border-fa-border p-3 text-left hover:border-fa-blue disabled:opacity-50"
            :disabled="capturing"
            @click="chooseSmartType('invoice')"
          >
            <i class="pi pi-file-pdf mt-0.5 text-lg text-fa-blue" />
            <span>
              <span class="block font-semibold">Formatted invoice</span>
              <span class="block text-xs text-fa-muted">A supplier PDF invoice (also reads the VAT number).</span>
            </span>
          </button>
        </div>
        <div v-if="capturing" class="mt-4 flex items-center gap-2 text-sm text-fa-muted">
          <i class="pi pi-spin pi-spinner" /> Uploading…
        </div>
        <p v-if="smartError" class="mt-3 text-xs text-[#c0392b]">{{ smartError }}</p>
      </Dialog>
      <input
        ref="smartFileInput"
        type="file"
        :accept="SMART_ACCEPT"
        class="hidden"
        @change="onSmartFilePicked"
      />

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
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
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
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
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
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
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
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
        </FormRow>

        <FormRow label="Description" label-for="description" required>
          <InputText
            id="description"
            v-model="form.description"
            class="w-full max-w-xl"
            :invalid="!!errors.description"
          />
          <p v-if="errors.description" class="text-xs text-[#c0392b]">{{ errors.description }}</p>
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
        </FormRow>

        <FormRow label="Supplier name" label-for="supplier">
          <InputText id="supplier" v-model="form.supplierName" class="w-72" />
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
        </FormRow>

        <FormRow label="Supplier VAT number" label-for="supplier-vat">
          <InputText id="supplier-vat" v-model="form.supplierVat" class="w-56" />
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
        </FormRow>

        <FormRow label="Invoice number" label-for="invoice">
          <InputText id="invoice" v-model="form.invoiceNumber" class="w-56" />
          <span v-if="ocrPolling" class="block text-xs text-fa-muted"><i class="pi pi-spin pi-spinner" /> reading…</span>
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
