<script setup lang="ts">
// Bills list — wired to GET /api/v1/bills. Mirrors InvoiceListView: AppLayout
// wrapper, a hand-rolled Tailwind table with the fa-* theme colours, and a
// loading/error/empty/data state machine.
//
// The list API returns only contact_id (no supplier name), so — like the invoices
// list — we also fetch contacts and join by id to show a readable supplier name. A
// row opens the bill's EDIT form directly (the bills module has no separate
// read-only detail view); a paid bill opens read-only there.
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Select from 'primevue/select'
import MultiSelect from 'primevue/multiselect'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { listBills } from '@/services/bills.service'
import { listContacts } from '@/services/contacts.service'
import { formatMoney, formatDate } from '@/lib/format'
import { billStatusClass } from '@/lib/billStatus'
import type { Bill } from '@/types/bill'
import type { Contact } from '@/types/contact'
import type { ApiError } from '@/lib/api'

const bills = ref<Bill[]>([])
// contact_id → display name, built from the contacts list for the Supplier column.
const contactNames = ref<Map<string, string>>(new Map())
const loading = ref(true)
const error = ref('')

const router = useRouter()

function newBill() {
  router.push('/bills/new')
}
function openBill(id: string) {
  router.push(`/bills/${id}/edit`)
}

// Status filter over the DERIVED display_status. Empty = show all.
const statusOptions = ['Unpaid', 'Part paid', 'Paid', 'Overdue', 'Zero Value']
const selectedStatuses = ref<string[]>([])

const filteredBills = computed(() =>
  selectedStatuses.value.length === 0
    ? bills.value
    : bills.value.filter((b) => selectedStatuses.value.includes(b.display_status)),
)

// Client-side pagination cap (the list arrives newest-first from the API).
const perPage = ref(25)
const perPageOptions = [25, 50, 100]
const paged = computed(() => filteredBills.value.slice(0, perPage.value))

// A contact's display name: company name, else person name, else email. Same
// precedence as the contacts / invoices / projects lists.
function contactDisplayName(c: Contact): string {
  const org = c.organisation_name?.trim()
  if (org) return org
  const person = `${c.first_name ?? ''} ${c.last_name ?? ''}`.trim()
  if (person) return person
  return c.email?.trim() || '(no name)'
}

function supplierName(b: Bill): string {
  return contactNames.value.get(b.contact_id) || '—'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    // Like the invoices list, fetch contacts alongside and build an id → name map
    // for the Supplier column. Both are org-scoped; a 401 on either is handled by
    // apiFetch (logout + redirect).
    const [billList, contactList] = await Promise.all([listBills(), listContacts()])
    bills.value = billList
    contactNames.value = new Map(contactList.map((c) => [c.id, contactDisplayName(c)]))
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load bills.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Bills</h1>
      <Button label="New Bill" @click="newBill" />
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading bills…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty -->
      <div v-else-if="bills.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No bills yet</p>
        <p class="text-sm text-fa-muted">Bills you record will appear here.</p>
      </div>

      <!-- Data -->
      <template v-else>
        <!-- Filter row -->
        <div class="flex flex-wrap items-center gap-2 border-b border-fa-border px-4 py-3">
          <MultiSelect
            v-model="selectedStatuses"
            :options="statusOptions"
            placeholder="All statuses"
            :max-selected-labels="3"
            class="w-full sm:w-72"
          />
        </div>

        <div class="overflow-x-auto">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Reference
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Supplier
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Bill date
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Due on
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Status
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                  Total
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                  Outstanding
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="b in paged" :key="b.id" class="group hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <button
                    type="button"
                    class="text-left font-semibold text-fa-blue hover:underline"
                    @click="openBill(b.id)"
                  >
                    {{ b.reference || '(no reference)' }}
                  </button>
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                  {{ supplierName(b) }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                  {{ formatDate(b.dated_on) }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                  {{ b.due_on ? formatDate(b.due_on) : '—' }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <span
                    class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
                    :class="billStatusClass(b.display_status)"
                    >{{ b.display_status }}</span
                  >
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums">
                  {{ formatMoney(b.total_value, b.currency) }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums">
                  {{ formatMoney(b.due_value, b.currency) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- No rows after filtering -->
        <div v-if="paged.length === 0" class="px-4 py-10 text-center text-sm text-fa-muted">
          No bills match these filters.
        </div>

        <!-- Per-page footer -->
        <div class="flex items-center gap-2 border-t border-fa-border px-4 py-3 text-sm text-fa-muted">
          <Select v-model="perPage" :options="perPageOptions" />
          <span>per page</span>
        </div>
      </template>
    </div>
  </AppLayout>
</template>
