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
import { formatMoney, formatDate, formatDateTime } from '@/lib/format'
import type { ExpenseDetail } from '@/types/expense'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const id = route.params.id as string

const expense = ref<ExpenseDetail | null>(null)
const loading = ref(true)
const error = ref<ApiError | null>(null)

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

onMounted(load)

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
  </AppLayout>
</template>
