<script setup lang="ts">
// Bank account transactions — the read-only statement view. Wired to
// GET /api/v1/bank-accounts/:id/transactions → { account, transactions }. Modelled
// on the FreeAgent account screen: a statement table (status icons, Money in/out, a
// server-computed running Balance + "Balance brought forward"), the 4 tabs as
// client-side filters with counts, and a right sidebar (Bank details + simplified
// Bank feed / For approval cards). "Edit details" routes to the account-edit form.
//
// Deferred (no data/feature yet): per-row category + VAT, the explain/reconcile flow,
// the live bank feed, statement upload, manual entry, period filter, search, pagination.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import Menu from 'primevue/menu'
import Select from 'primevue/select'
import InputText from 'primevue/inputtext'
import type { MenuItem } from 'primevue/menuitem'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import { getBankAccountTransactions, deleteBankAccount, listBankAccounts } from '@/services/bank-accounts.service'
import {
  listTransactionTypes,
  listCategoriesForType,
  listExplanations,
  createExplanation,
  updateExplanation,
  deleteExplanation,
} from '@/services/explanation.service'
import { listVatRates } from '@/services/expenses.service'
import { listMembers } from '@/services/members.service'
import { formatMoney, formatDate } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'
import type { BankAccount, BankTransaction } from '@/types/bank-account'
import type { TransactionType, ExplanationCategory, Explanation, TransactionExplanations, CreateExplanationRequest } from '@/types/explanation'
import type { VatRate } from '@/types/expense'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const id = String(route.params.id)

const account = ref<BankAccount | null>(null)
const transactions = ref<BankTransaction[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await getBankAccountTransactions(id)
    account.value = data.account
    transactions.value = data.transactions
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load transactions.'
  } finally {
    loading.value = false
  }
}
onMounted(load)

function editDetails() {
  router.push(`/bank-accounts/${id}/edit`)
}
function uploadStatement() {
  router.push(`/bank-accounts/${id}/transactions/import`)
}

// --- "More ▾" menu (owner/admin only; the backend enforces it) ---
const moreMenu = ref()
const moreItems = computed<MenuItem[]>(() => [
  { label: 'Add transaction', icon: 'pi pi-plus', command: () => router.push(`/bank-accounts/${id}/transactions/new`) },
  { label: 'Delete account', icon: 'pi pi-trash', command: () => confirmDeleteAccount() },
])
function toggleMore(event: Event) {
  moreMenu.value?.toggle(event)
}
async function confirmDeleteAccount() {
  if (!window.confirm('Delete this bank account? Its transactions are kept for audit, but the account is hidden.')) return
  try {
    await deleteBankAccount(id)
    router.push('/bank-accounts')
  } catch (err) {
    window.alert((err as ApiError)?.message ?? 'Could not delete the account.')
  }
}

// Per-row Edit (manual lines only) → the transaction entry view.
function editTransaction(t: BankTransaction) {
  router.push(`/bank-accounts/${id}/transactions/${t.id}/edit`)
}

// --- tabs (client-side filters over the loaded list) ---
type Tab = 'all' | 'unexplained' | 'for_approval' | 'manual'
const tab = ref<Tab>('all')
const tabDefs: { key: Tab; label: string }[] = [
  { key: 'all', label: 'All transactions' },
  { key: 'unexplained', label: 'Unexplained' },
  { key: 'for_approval', label: 'For approval' },
  { key: 'manual', label: 'Manually added' },
]

function inTab(t: BankTransaction, which: Tab): boolean {
  switch (which) {
    case 'unexplained':
      return t.status === 'unexplained'
    case 'for_approval':
      return t.status === 'for_approval'
    case 'manual':
      return t.source === 'manual'
    default:
      return true
  }
}
function tabCount(key: Tab): number {
  return transactions.value.filter((t) => inTab(t, key)).length
}
const visible = computed(() => transactions.value.filter((t) => inTab(t, tab.value)))

// --- status icon (matches the bottom legend) ---
function statusMeta(t: BankTransaction): { icon: string; cls: string; label: string } {
  if (t.source === 'manual') return { icon: 'pi-user', cls: 'text-[#8e44ad]', label: 'Manually added' }
  switch (t.status) {
    case 'explained':
      return { icon: 'pi-check', cls: 'text-fa-green', label: 'Explained' }
    case 'for_approval':
      return { icon: 'pi-eye', cls: 'text-[#e67e22]', label: 'Marked for approval' }
    default:
      return { icon: 'pi-question-circle', cls: 'text-[#c0392b]', label: 'Unexplained' }
  }
}

// Table amounts: thousands separators, NO symbol (the screenshot's Money/Balance
// columns). The sidebar total uses formatMoney (with the £ symbol).
function amount(pounds: string | null | undefined): string {
  if (pounds == null || pounds === '') return ''
  const n = Number(pounds)
  if (Number.isNaN(n)) return pounds
  return n.toLocaleString('en-GB', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

// =============================================================================
// EXPLAIN / RECONCILE — the inline expanding panel (owner/admin only)
// Clicking a row opens a panel that lists its explanations + a "remaining to
// explain" indicator + an add/edit form. A line can be SPLIT across several
// explanations. Reference data (types, VAT rates, accounts, members) loads once.
// =============================================================================
const expandedId = ref<string | null>(null)
const panelLoading = ref(false)
const panelError = ref('')
const explanations = ref<Explanation[]>([])
const remaining = ref('0.00') // signed pounds still to explain (the line's unexplained_amount)

// reference data (lazy-loaded once on first expand)
const txnTypes = ref<TransactionType[]>([])
const vatRates = ref<VatRate[]>([])
const otherAccounts = ref<{ id: string; name: string }[]>([])
const members = ref<{ id: string; name: string }[]>([])
const refLoaded = ref(false)

// the add/edit form
const form = reactive({ type: '', categoryId: '', transferAccountId: '', paidUserId: '', amount: '', vatRateId: '', description: '' })
const editingId = ref<string | null>(null)
const saving = ref(false)
const formError = ref('')
const catsForType = ref<ExplanationCategory[]>([])

const expandedTxn = computed(() => transactions.value.find((t) => t.id === expandedId.value) ?? null)
const lineDirection = computed(() => (expandedTxn.value?.money_out ? 'out' : 'in'))
const selectedType = computed(() => txnTypes.value.find((t) => t.code === form.type) ?? null)
const entityLink = computed(() => selectedType.value?.entity_link ?? 'NONE')
const typeOptions = computed(() =>
  txnTypes.value.filter((t) => t.direction === lineDirection.value && t.supported).map((t) => ({ label: t.name, value: t.code })),
)
const categoryOptions = computed(() => catsForType.value.map((c) => ({ label: `${c.name} (${c.nominal_code})`, value: c.id })))
const accountOptions = computed(() => otherAccounts.value.map((a) => ({ label: a.name, value: a.id })))
const memberOptions = computed(() => members.value.map((m) => ({ label: m.name, value: m.id })))
const vatOptions = computed(() => [{ label: 'No VAT', value: '' }, ...vatRates.value.map((r) => ({ label: `${r.name} (${r.rate})`, value: r.id }))])
const remainingAbs = computed(() => Math.abs(Number(remaining.value || '0')).toFixed(2))
const fullyExplained = computed(() => Number(remaining.value || '0') === 0)

function typeName(code: string): string {
  return txnTypes.value.find((t) => t.code === code)?.name ?? code
}
function memberName(m: { first_name: string; last_name: string; email: string }): string {
  return [m.first_name, m.last_name].filter(Boolean).join(' ') || m.email || 'User'
}

async function loadRefData() {
  if (refLoaded.value) return
  const [types, vats, accounts, mems] = await Promise.all([
    listTransactionTypes(),
    listVatRates(),
    listBankAccounts(),
    listMembers().catch(() => []),
  ])
  txnTypes.value = types
  vatRates.value = vats
  otherAccounts.value = accounts.filter((a) => a.id !== id).map((a) => ({ id: a.id, name: a.name }))
  members.value = mems.filter((m) => m.status === 'active').map((m) => ({ id: m.user_id, name: memberName(m) }))
  refLoaded.value = true
}

async function toggleExpand(t: BankTransaction) {
  if (expandedId.value === t.id) {
    expandedId.value = null
    return
  }
  expandedId.value = t.id
  resetForm()
  panelLoading.value = true
  panelError.value = ''
  try {
    await loadRefData()
    applyPanel(await listExplanations(id, t.id))
    form.amount = remainingAbs.value
  } catch (err) {
    panelError.value = (err as ApiError)?.message ?? 'Could not load explanations.'
  } finally {
    panelLoading.value = false
  }
}

// applyPanel updates the panel + patches the statement row's status/remaining from a response.
function applyPanel(data: TransactionExplanations) {
  explanations.value = data.explanations ?? []
  remaining.value = data.unexplained_amount
  const row = transactions.value.find((t) => t.id === data.transaction_id)
  if (row) {
    row.status = data.status
    row.unexplained_amount = data.unexplained_amount
  }
}

function resetForm() {
  editingId.value = null
  form.type = ''
  form.categoryId = ''
  form.transferAccountId = ''
  form.paidUserId = ''
  form.amount = ''
  form.vatRateId = ''
  form.description = ''
  catsForType.value = []
  formError.value = ''
}

// onTypeChange loads the categories the chosen type offers (category + user types).
async function onTypeChange() {
  form.categoryId = ''
  form.transferAccountId = ''
  form.paidUserId = ''
  catsForType.value = []
  if (!form.type) return
  if (entityLink.value !== 'BANK_ACCOUNT') {
    try {
      catsForType.value = await listCategoriesForType(form.type)
    } catch {
      catsForType.value = []
    }
  }
}

// onCategoryChange pre-selects the VAT rate from the category's default_vat.
function onCategoryChange() {
  const cat = catsForType.value.find((c) => c.id === form.categoryId)
  form.vatRateId = vatRateForDefault(cat?.default_vat)
}
function vatRateForDefault(defaultVat?: string | null): string {
  const keyword: Record<string, string> = { STANDARD: 'standard', REDUCED: 'reduced', ZERO: 'zero', EXEMPT: 'exempt' }
  const kw = defaultVat ? keyword[defaultVat] : undefined
  if (!kw) return '' // OUTSIDE_SCOPE / unset → no VAT
  return vatRates.value.find((r) => r.name.toLowerCase().includes(kw))?.id ?? ''
}

async function submitForm() {
  formError.value = ''
  if (!form.type) {
    formError.value = 'Choose a type.'
    return
  }
  if (!form.amount || Number(form.amount) <= 0) {
    formError.value = 'Enter an amount greater than zero.'
    return
  }
  const payload: CreateExplanationRequest = { type: form.type, amount: form.amount }
  if (entityLink.value === 'BANK_ACCOUNT') {
    if (!form.transferAccountId) {
      formError.value = 'Choose the other account.'
      return
    }
    payload.transfer_bank_account_id = form.transferAccountId
  } else if (entityLink.value === 'USER') {
    if (!form.paidUserId || !form.categoryId) {
      formError.value = 'Choose a user and an account.'
      return
    }
    payload.paid_user_id = form.paidUserId
    payload.category_id = form.categoryId
  } else {
    if (!form.categoryId) {
      formError.value = 'Choose a category.'
      return
    }
    payload.category_id = form.categoryId
  }
  if (form.vatRateId) payload.vat_rate_id = form.vatRateId
  if (form.description) payload.description = form.description

  saving.value = true
  try {
    const data = editingId.value
      ? await updateExplanation(id, expandedId.value!, editingId.value, payload)
      : await createExplanation(id, expandedId.value!, payload)
    applyPanel(data)
    resetForm()
    form.amount = remainingAbs.value
  } catch (err) {
    formError.value = (err as ApiError)?.message ?? 'Could not save the explanation.'
  } finally {
    saving.value = false
  }
}

async function startEdit(e: Explanation) {
  editingId.value = e.id
  form.type = e.type
  form.amount = e.amount
  form.description = e.description ?? ''
  form.vatRateId = e.vat_rate_id ?? ''
  await onTypeChange()
  form.categoryId = e.category_id ?? ''
  form.transferAccountId = e.transfer_bank_account_id ?? ''
  form.paidUserId = e.paid_user_id ?? ''
}

function cancelEdit() {
  resetForm()
  form.amount = remainingAbs.value
}

async function removeExplanation(e: Explanation) {
  panelError.value = ''
  try {
    applyPanel(await deleteExplanation(id, expandedId.value!, e.id))
    if (editingId.value === e.id) resetForm()
    if (!editingId.value) form.amount = remainingAbs.value
  } catch (err) {
    panelError.value = (err as ApiError)?.message ?? 'Could not remove the explanation.'
  }
}
</script>

<template>
  <AppLayout>
    <!-- Loading -->
    <div v-if="loading" class="px-4 py-16 text-center text-fa-muted">
      <i class="pi pi-spin pi-spinner mr-2" />Loading transactions…
    </div>

    <!-- Error -->
    <div v-else-if="error" class="px-4 py-16 text-center">
      <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
      <Button label="Retry" severity="secondary" outlined @click="load" />
    </div>

    <template v-else-if="account">
      <!-- Header: account name + actions -->
      <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
        <h1 class="text-[22px] font-bold">{{ account.name }}</h1>
        <div class="flex gap-2.5">
          <Button
            v-if="auth.isOrgAdmin"
            label="Upload statement"
            severity="secondary"
            outlined
            @click="uploadStatement"
          />
          <Button label="Edit details" severity="secondary" outlined @click="editDetails" />
          <!-- "More" hosts Add transaction + Delete account — owner/admin only. -->
          <template v-if="auth.isOrgAdmin">
            <Button
              label="More"
              icon="pi pi-angle-down"
              icon-pos="right"
              severity="secondary"
              outlined
              aria-haspopup="true"
              @click="toggleMore"
            />
            <Menu ref="moreMenu" :model="moreItems" :popup="true" />
          </template>
        </div>
      </div>

      <div class="grid gap-5 lg:grid-cols-[minmax(0,1fr)_300px]">
        <!-- MAIN: tabs + table + legend -->
        <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
          <!-- Tabs -->
          <div class="flex flex-wrap items-center gap-1 border-b border-fa-border px-2 py-1.5">
            <button
              v-for="t in tabDefs"
              :key="t.key"
              type="button"
              class="rounded px-3 py-2 text-sm font-semibold"
              :class="tab === t.key ? 'bg-fa-blue text-white' : 'text-fa-blue hover:bg-[#eef4fb]'"
              @click="tab = t.key"
            >
              {{ t.label }}
              <span
                v-if="t.key !== 'all' && tabCount(t.key) > 0"
                class="ml-1 inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-xs"
                :class="t.key === 'for_approval' ? 'bg-[#e67e22] text-white' : 'bg-[#eef1f4] text-fa-muted'"
              >
                {{ tabCount(t.key) }}
              </span>
            </button>
          </div>

          <!-- Table -->
          <div class="overflow-x-auto">
            <table class="w-full border-collapse text-sm">
              <thead>
                <tr>
                  <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Date</th>
                  <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">Description</th>
                  <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Money in</th>
                  <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Money out</th>
                  <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">Balance</th>
                  <th class="border-b border-fa-border px-4 py-3" />
                </tr>
              </thead>
              <tbody>
                <!-- Balance brought forward (the opening balance; on the All tab) -->
                <tr v-if="tab === 'all'">
                  <td class="border-b border-[#eef1f4] px-4 py-3" />
                  <td class="border-b border-[#eef1f4] px-4 py-3 italic text-fa-muted">Balance brought forward</td>
                  <td class="border-b border-[#eef1f4] px-4 py-3" />
                  <td class="border-b border-[#eef1f4] px-4 py-3" />
                  <td class="border-b border-[#eef1f4] px-4 py-3 text-right font-semibold tabular-nums">
                    {{ amount(account.opening_balance) }}
                  </td>
                  <td class="border-b border-[#eef1f4] px-4 py-3" />
                </tr>

                <template v-for="t in visible" :key="t.id">
                  <tr
                    class="group hover:bg-[#f7fafc]"
                    :class="{ 'cursor-pointer': auth.isOrgAdmin, 'bg-[#f7fafc]': expandedId === t.id }"
                    @click="auth.isOrgAdmin ? toggleExpand(t) : null"
                  >
                    <td class="whitespace-nowrap border-b border-[#eef1f4] px-4 py-3 align-top">
                      <div class="flex items-center gap-2">
                        <i
                          class="pi text-[13px]"
                          :class="[statusMeta(t).icon, statusMeta(t).cls]"
                          :title="statusMeta(t).label"
                        />
                        <span>{{ formatDate(t.dated_on) }}</span>
                      </div>
                    </td>
                    <td class="border-b border-[#eef1f4] px-4 py-3 align-top">
                      <div class="font-semibold text-fa-text">{{ t.description || '—' }}</div>
                      <div v-if="t.bank_memo" class="text-xs text-fa-muted">{{ t.bank_memo }}</div>
                    </td>
                    <td class="border-b border-[#eef1f4] px-4 py-3 text-right align-top tabular-nums">{{ amount(t.money_in) }}</td>
                    <td class="border-b border-[#eef1f4] px-4 py-3 text-right align-top tabular-nums">{{ amount(t.money_out) }}</td>
                    <td class="border-b border-[#eef1f4] px-4 py-3 text-right align-top tabular-nums">{{ amount(t.running_balance) }}</td>
                    <td class="whitespace-nowrap border-b border-[#eef1f4] px-4 py-3 text-right align-top">
                      <button
                        v-if="t.is_manual && auth.isOrgAdmin"
                        type="button"
                        class="invisible text-fa-blue hover:underline group-hover:visible"
                        @click.stop="editTransaction(t)"
                      >
                        Edit
                      </button>
                    </td>
                  </tr>

                  <!-- Inline explain / reconcile panel -->
                  <tr v-if="expandedId === t.id">
                    <td colspan="6" class="border-b border-fa-border bg-[#f7fafc] px-4 py-4">
                      <div v-if="panelLoading" class="text-sm text-fa-muted">
                        <i class="pi pi-spin pi-spinner mr-2" />Loading…
                      </div>
                      <div v-else>
                        <p v-if="panelError" class="mb-2 text-sm text-[#c0392b]">{{ panelError }}</p>

                        <!-- existing explanations -->
                        <table v-if="explanations.length" class="mb-3 w-full text-sm">
                          <tbody>
                            <tr v-for="e in explanations" :key="e.id" class="border-b border-[#eef1f4]">
                              <td class="py-1.5 font-semibold">{{ typeName(e.type) }}</td>
                              <td class="py-1.5 text-fa-muted">
                                {{ e.category_name || e.transfer_account_name || e.paid_user_name || '—' }}
                              </td>
                              <td class="py-1.5 text-right tabular-nums">
                                £{{ e.amount }}<span v-if="Number(e.vat_value) > 0" class="text-fa-muted"> (incl. £{{ e.vat_value }} VAT)</span>
                              </td>
                              <td class="whitespace-nowrap py-1.5 text-right">
                                <button type="button" class="text-fa-blue hover:underline" @click="startEdit(e)">Edit</button>
                                <button type="button" class="ml-3 text-[#c0392b] hover:underline" @click="removeExplanation(e)">Remove</button>
                              </td>
                            </tr>
                          </tbody>
                        </table>

                        <!-- remaining indicator -->
                        <p class="mb-3 text-sm font-semibold" :class="fullyExplained ? 'text-fa-green' : 'text-[#c0392b]'">
                          {{ fullyExplained ? 'Fully explained' : `£${remainingAbs} left to explain` }}
                        </p>

                        <!-- add / edit form -->
                        <div v-if="!fullyExplained || editingId" class="rounded border border-fa-border bg-white p-3">
                          <div class="flex flex-wrap items-end gap-2.5">
                            <label class="flex flex-col gap-1 text-xs text-fa-muted">Type
                              <Select v-model="form.type" :options="typeOptions" option-label="label" option-value="value" placeholder="Choose…" class="w-52" @change="onTypeChange" />
                            </label>

                            <label v-if="entityLink === 'BANK_ACCOUNT'" class="flex flex-col gap-1 text-xs text-fa-muted">Account
                              <Select v-model="form.transferAccountId" :options="accountOptions" option-label="label" option-value="value" placeholder="Account" class="w-52" />
                            </label>
                            <template v-else-if="entityLink === 'USER'">
                              <label class="flex flex-col gap-1 text-xs text-fa-muted">User
                                <Select v-model="form.paidUserId" :options="memberOptions" option-label="label" option-value="value" placeholder="User" class="w-44" />
                              </label>
                              <label class="flex flex-col gap-1 text-xs text-fa-muted">Account
                                <Select v-model="form.categoryId" :options="categoryOptions" option-label="label" option-value="value" placeholder="Account" class="w-52" @change="onCategoryChange" />
                              </label>
                            </template>
                            <label v-else class="flex flex-col gap-1 text-xs text-fa-muted">Category
                              <Select v-model="form.categoryId" :options="categoryOptions" option-label="label" option-value="value" placeholder="Category" filter class="w-60" @change="onCategoryChange" />
                            </label>

                            <label class="flex flex-col gap-1 text-xs text-fa-muted">Amount (£)
                              <InputText v-model="form.amount" class="w-28" />
                            </label>
                            <label class="flex flex-col gap-1 text-xs text-fa-muted">VAT
                              <Select v-model="form.vatRateId" :options="vatOptions" option-label="label" option-value="value" class="w-40" />
                            </label>
                            <label class="flex flex-col gap-1 text-xs text-fa-muted">Description
                              <InputText v-model="form.description" placeholder="Optional" class="w-48" />
                            </label>

                            <Button :label="editingId ? 'Save' : 'Add'" :loading="saving" @click="submitForm" />
                            <Button v-if="editingId" label="Cancel" severity="secondary" outlined @click="cancelEdit" />
                          </div>
                          <p v-if="formError" class="mt-2 text-sm text-[#c0392b]">{{ formError }}</p>
                        </div>
                      </div>
                    </td>
                  </tr>
                </template>

                <tr v-if="visible.length === 0">
                  <td colspan="6" class="px-4 py-12 text-center text-fa-muted">No transactions in this view.</td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- Legend -->
          <div class="flex flex-wrap items-center gap-x-5 gap-y-1.5 border-t border-fa-border px-4 py-3 text-xs text-fa-muted">
            <span class="inline-flex items-center gap-1.5"><i class="pi pi-check text-fa-green" /> Explained</span>
            <span class="inline-flex items-center gap-1.5"><i class="pi pi-question-circle text-[#c0392b]" /> Unexplained</span>
            <span class="inline-flex items-center gap-1.5"><i class="pi pi-user text-[#8e44ad]" /> Manually added</span>
            <span class="inline-flex items-center gap-1.5"><i class="pi pi-eye text-[#e67e22]" /> Marked for approval</span>
          </div>
        </div>

        <!-- SIDEBAR -->
        <div class="flex flex-col gap-5">
          <FaCard title="Bank details">
            <div class="text-2xl font-bold">{{ formatMoney(account.current_balance, account.currency) }}</div>
            <div class="mb-3 text-xs text-fa-muted">Total balance</div>
            <dl class="space-y-2 text-sm">
              <div v-if="account.bank_name">
                <dt class="text-fa-muted">Bank</dt>
                <dd class="font-semibold">{{ account.bank_name }}</dd>
              </div>
              <div v-if="account.sort_code">
                <dt class="text-fa-muted">Sort code</dt>
                <dd class="font-semibold">{{ account.sort_code }}</dd>
              </div>
              <div v-if="account.account_number">
                <dt class="text-fa-muted">Account number</dt>
                <dd class="font-semibold">{{ account.account_number }}</dd>
              </div>
              <div v-if="account.iban">
                <dt class="text-fa-muted">IBAN</dt>
                <dd class="font-semibold">{{ account.iban }}</dd>
              </div>
            </dl>
          </FaCard>

          <FaCard title="Bank feed">
            <p class="text-sm text-fa-muted">No bank feed connected.</p>
          </FaCard>

          <FaCard title="For approval">
            <button
              type="button"
              class="flex w-full items-center justify-between text-sm text-fa-blue hover:underline"
              @click="tab = 'for_approval'"
            >
              <span>Total for approval</span>
              <span
                class="inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-[#e67e22] px-1.5 text-xs text-white"
              >
                {{ tabCount('for_approval') }}
              </span>
            </button>
          </FaCard>
        </div>
      </div>
    </template>
  </AppLayout>
</template>
