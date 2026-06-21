<script setup lang="ts">
// Bank accounts list — wired to GET /api/v1/bank-accounts. Mirrors ContactListView:
// AppLayout wrapper, a hand-rolled Tailwind table with the fa-* theme, and a
// loading/error/empty/data state machine.
//
// The Account balance column is the REAL derived balance (opening + Σ transactions)
// the backend computes. The FreeAgent "Bank feed / For approval / Unexplained"
// columns are deferred until transactions + feeds land (see BACKLOG.md).
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { listBankAccounts } from '@/services/bank-accounts.service'
import { formatMoney } from '@/lib/format'
import type { BankAccount } from '@/types/bank-account'
import type { ApiError } from '@/lib/api'

const router = useRouter()

const accounts = ref<BankAccount[]>([])
const loading = ref(true)
const error = ref('')

function newAccount() {
  router.push('/bank-accounts/new')
}
function openTransactions(id: string) {
  router.push(`/bank-accounts/${id}`)
}

// The identifier line under the account name: UK account/sort, else IBAN, else —.
function accountRef(a: BankAccount): string {
  const parts = [a.account_number?.trim(), a.sort_code?.trim()].filter(Boolean)
  if (parts.length) return parts.join('  ')
  return a.iban?.trim() || '—'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    accounts.value = await listBankAccounts()
  } catch (err) {
    // A 401 is already handled by apiFetch (logout + redirect); anything else
    // shows here with a retry.
    error.value = (err as ApiError)?.message ?? 'Could not load bank accounts.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Bank Accounts</h1>
      <Button label="Add New Account" @click="newAccount" />
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading bank accounts…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty -->
      <div v-else-if="accounts.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No bank accounts yet</p>
        <p class="mb-4 text-sm text-fa-muted">Add your first bank account to see it here.</p>
        <Button label="Add New Account" @click="newAccount" />
      </div>

      <!-- Data -->
      <template v-else>
        <div class="overflow-x-auto">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Account details
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Currency
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Status
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                  Account balance
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="a in accounts" :key="a.id" class="group hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <div class="flex items-center gap-2.5">
                    <i
                      class="pi text-[13px]"
                      :class="a.is_primary ? 'pi-star-fill text-fa-blue' : 'pi-star text-[#c2c9d1]'"
                      :title="a.is_primary ? 'Primary account' : ''"
                    />
                    <div>
                      <button
                        type="button"
                        class="text-left font-semibold text-fa-blue hover:underline"
                        @click="openTransactions(a.id)"
                      >
                        {{ a.name }}
                      </button>
                      <div class="text-xs text-fa-muted">{{ accountRef(a) }}</div>
                    </div>
                  </div>
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                  {{ a.currency }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <span class="capitalize" :class="a.status === 'active' ? 'text-fa-green' : 'text-fa-muted'">
                    {{ a.status }}
                  </span>
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle font-semibold tabular-nums">
                  {{ formatMoney(a.current_balance, a.currency) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- Primary-account legend (matches the screenshot's footer) -->
        <div class="flex items-center gap-1.5 border-t border-fa-border px-4 py-3 text-xs text-fa-muted">
          <i class="pi pi-star-fill text-fa-blue" /> Primary account
        </div>
      </template>
    </div>
  </AppLayout>
</template>
