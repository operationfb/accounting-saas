<script setup lang="ts">
// Single expense — the "detail" view, wired to GET /api/v1/expenses/:id (rich
// data from the v_expenses_full view).
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import StatusTag from '@/components/StatusTag.vue'
import { getExpense } from '@/services/expenses.service'
import { listAttachments, getDownloadUrl } from '@/services/attachments.service'
import { formatMoney, formatDate, formatDateTime, formatBytes } from '@/lib/format'
import type { ExpenseDetail } from '@/types/expense'
import type { Attachment } from '@/types/attachment'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const id = route.params.id as string

const expense = ref<ExpenseDetail | null>(null)
const loading = ref(true)
const error = ref<ApiError | null>(null)

// Attachments load independently of the expense (separate endpoint).
const attachments = ref<Attachment[]>([])
const attachmentsLoading = ref(true)
const attachmentsError = ref('')
const previewError = ref('')

// One-off notice when we landed here right after a create whose receipts didn't
// all upload (ExpenseEntryView passes ?attach=partial).
const uploadNotice = computed(() => route.query.attach === 'partial')

async function load() {
  loading.value = true
  error.value = null
  try {
    expense.value = await getExpense(id)
  } catch (err) {
    // A 401 is already handled by apiFetch (logout + redirect). Anything else
    // (404 / 403 / 422 / 500) lands here and is shown as an error state.
    error.value = err as ApiError
    expense.value = null
  } finally {
    loading.value = false
  }
}

async function loadAttachments() {
  attachmentsLoading.value = true
  attachmentsError.value = ''
  try {
    attachments.value = await listAttachments(id)
  } catch (err) {
    // 401 is handled by apiFetch; other failures show inline (the expense card
    // carries its own 404/403, so we don't double up on those here).
    attachmentsError.value = (err as ApiError)?.message ?? 'Could not load attachments.'
  } finally {
    attachmentsLoading.value = false
  }
}

// Open a receipt in a new tab. Open the blank tab SYNCHRONOUSLY (so the pop-up
// blocker permits it), then point it at the short-lived signed URL once it loads.
function openAttachment(att: Attachment) {
  previewError.value = ''
  const w = window.open('', '_blank')
  getDownloadUrl(id, att.id)
    .then((url) => {
      if (w) w.location.href = url
      else window.location.href = url // pop-up blocked → use this tab
    })
    .catch((err) => {
      if (w) w.close()
      previewError.value = (err as ApiError)?.message ?? 'Could not open this file.'
    })
}

function iconFor(type: string): string {
  if (type === 'application/pdf') return 'pi pi-file-pdf'
  if (type.startsWith('image/')) return 'pi pi-image'
  return 'pi pi-file'
}

onMounted(() => {
  load()
  loadAttachments()
})

const notFound = computed(() => error.value?.status === 404)

// label/value rows built from the fetched expense; optional/empty fields are
// shown only when present.
const rows = computed(() => {
  const e = expense.value
  if (!e) return []
  const out: { label: string; value: string }[] = [
    { label: 'Description', value: e.description },
    { label: 'Category', value: `${e.category_name} (${e.category_nominal_code})` },
    { label: 'Dated', value: formatDate(e.dated_on) },
    { label: 'Currency', value: e.currency },
    { label: 'Total value', value: formatMoney(e.gross_value, e.currency) },
  ]
  if (e.vat_rate) out.push({ label: 'VAT rate', value: e.vat_rate })
  out.push({ label: 'VAT amount', value: formatMoney(e.vat_value, e.currency) })
  out.push({ label: 'VAT status', value: e.vat_status })
  out.push({ label: 'EC status', value: e.ec_status })

  // FX rows only make sense when the expense currency differs from the home one.
  if (e.currency !== e.native_currency) {
    out.push({ label: `Total (${e.native_currency})`, value: formatMoney(e.native_gross_value, e.native_currency) })
    out.push({ label: `VAT (${e.native_currency})`, value: formatMoney(e.native_vat_value, e.native_currency) })
    if (e.exchange_rate) out.push({ label: 'Exchange rate', value: e.exchange_rate })
  }

  if (e.supplier_name) out.push({ label: 'Supplier name', value: e.supplier_name })
  if (e.supplier_vat_number) out.push({ label: 'Supplier VAT number', value: e.supplier_vat_number })
  if (e.invoice_number) out.push({ label: 'Invoice number', value: e.invoice_number })
  if (e.receipt_reference) out.push({ label: 'Receipt reference', value: e.receipt_reference })

  if (e.rebill_type) out.push({ label: 'Rebill type', value: e.rebill_type })
  if (e.rebill_factor) out.push({ label: 'Rebill factor', value: e.rebill_factor })

  if (e.submitted_at) out.push({ label: 'Submitted', value: formatDateTime(e.submitted_at) })
  if (e.approved_at) out.push({ label: 'Approved', value: formatDateTime(e.approved_at) })
  if (e.paid_at) out.push({ label: 'Paid', value: formatDateTime(e.paid_at) })

  out.push({ label: 'Created', value: formatDateTime(e.created_at) })
  out.push({ label: 'Last updated', value: formatDateTime(e.updated_at) })
  return out
})

function backToList() {
  router.push('/expenses')
}
</script>

<template>
  <AppLayout>
    <div
      v-if="uploadNotice"
      class="mb-4 rounded border border-[#f0e0b6] bg-[#fdf6e3] px-3 py-2 text-sm text-[#8a6d3b]"
      role="status"
    >
      Some receipts didn’t finish uploading. You can add them by editing this expense.
    </div>

    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <h1 class="text-[22px] font-bold">Expense</h1>
        <StatusTag v-if="expense" :status="expense.status" />
      </div>
      <div class="flex gap-2.5">
        <!-- Edit only for editable statuses (the backend enforces DRAFT/REJECTED). -->
        <Button
          v-if="expense && (expense.status === 'DRAFT' || expense.status === 'REJECTED')"
          label="Edit"
          icon="pi pi-pencil"
          @click="router.push(`/expenses/${id}/edit`)"
        />
        <Button label="Back to list" severity="secondary" outlined @click="backToList" />
      </div>
    </div>

    <FaCard title="Expense details">
      <!-- Loading -->
      <div v-if="loading" class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>

      <!-- Error: 404 / 403 / 422 / other -->
      <div v-else-if="error" class="py-10 text-center">
        <p class="mb-1 font-semibold">
          {{ notFound ? 'Expense not found' : 'Could not load this expense' }}
        </p>
        <p class="mb-4 text-sm text-fa-muted">{{ error.message }}</p>
        <Button label="Back to list" severity="secondary" outlined @click="backToList" />
      </div>

      <!-- Data -->
      <dl v-else-if="expense">
        <div
          v-for="row in rows"
          :key="row.label"
          class="grid grid-cols-1 gap-0.5 border-b border-[#eef1f4] py-[9px] last:border-b-0 sm:grid-cols-[190px_minmax(0,1fr)] sm:gap-4"
        >
          <dt class="text-sm text-fa-muted sm:text-right">{{ row.label }}</dt>
          <dd class="text-sm text-fa-text">{{ row.value }}</dd>
        </div>
      </dl>
    </FaCard>

    <!-- Attachments (read-only) — view/download receipts. Adding, removing and
         choosing the primary live on the Edit page. Only shown once the expense
         itself loaded, so a 404/403 doesn't surface twice. -->
    <FaCard v-if="expense" title="Attachments">
      <div v-if="attachmentsLoading" class="py-3 text-sm text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
      <p v-else-if="attachmentsError" class="py-2 text-xs text-[#c0392b]">
        {{ attachmentsError }}
        <button type="button" class="underline" @click="loadAttachments">Retry</button>
      </p>
      <template v-else>
        <p v-if="previewError" class="py-1 text-xs text-[#c0392b]">{{ previewError }}</p>
        <ul v-if="attachments.length">
          <!-- Indent each row to the Expense-details value column (190px label
               column + 16px gap-4 = 206px) so the files line up with the values
               in the card above. Only at sm+ (where the details card becomes
               two-column); the <li> stays full-width, so the row dividers still
               span the whole card rather than indenting with the content. -->
          <li
            v-for="att in attachments"
            :key="att.id"
            class="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-[#eef1f4] py-2 last:border-b-0 sm:pl-[206px]"
          >
            <i :class="iconFor(att.content_type)" class="text-fa-muted" />
            <button
              type="button"
              class="font-semibold text-fa-blue hover:underline"
              @click="openAttachment(att)"
            >
              {{ att.file_name }}
            </button>
            <span class="text-xs text-fa-muted">{{ formatBytes(att.file_size_bytes) }}</span>
            <span
              v-if="att.is_primary"
              class="rounded bg-[#eaf7e6] px-1.5 py-0.5 text-[11px] font-semibold text-[#3f8038]"
            >
              Primary
            </span>
            <span v-if="att.description" class="basis-full pl-7 text-xs text-fa-muted">
              {{ att.description }}
            </span>
          </li>
        </ul>
        <p v-else class="py-2 text-sm text-fa-muted">No attachments.</p>
      </template>
    </FaCard>
  </AppLayout>
</template>
