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
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import { getBankAccountTransactions } from '@/services/bank-accounts.service'
import { formatMoney, formatDate } from '@/lib/format'
import type { BankAccount, BankTransaction } from '@/types/bank-account'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const router = useRouter()
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
          <Button label="Upload statement" severity="secondary" outlined disabled />
          <Button label="Edit details" severity="secondary" outlined @click="editDetails" />
          <Button
            label="More"
            icon="pi pi-angle-down"
            icon-pos="right"
            severity="secondary"
            outlined
            disabled
          />
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
                </tr>

                <tr v-for="t in visible" :key="t.id" class="hover:bg-[#f7fafc]">
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
                </tr>

                <tr v-if="visible.length === 0">
                  <td colspan="5" class="px-4 py-12 text-center text-fa-muted">No transactions in this view.</td>
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
