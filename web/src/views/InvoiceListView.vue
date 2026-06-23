<script setup lang="ts">
// Invoices list — wired to GET /api/v1/invoices. Mirrors ProjectListView/
// ContactListView: AppLayout wrapper, a hand-rolled Tailwind table with the fa-*
// theme colours, and a loading/error/empty/data state machine.
//
// The list API returns only contact_id (no contact name), so — like the projects
// list — we also fetch contacts and join by id to show a readable client name.
// "New Invoice" opens the create form; a reference opens the invoice detail.
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Select from 'primevue/select'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { listInvoices } from '@/services/invoices.service'
import { listContacts } from '@/services/contacts.service'
import { formatMoney, formatDate } from '@/lib/format'
import { invoiceStatusClass } from '@/lib/invoiceStatus'
import type { Invoice } from '@/types/invoice'
import type { Contact } from '@/types/contact'
import type { ApiError } from '@/lib/api'

const invoices = ref<Invoice[]>([])
// contact_id → display name, built from the contacts list for the Contact column.
const contactNames = ref<Map<string, string>>(new Map())
const loading = ref(true)
const error = ref('')

const router = useRouter()

function newInvoice() {
  router.push('/invoices/new')
}
function openDetail(id: string) {
  router.push(`/invoices/${id}`)
}

// Client-side pagination cap (the list arrives newest-first from the API).
const perPage = ref(25)
const perPageOptions = [25, 50, 100]
const paged = computed(() => invoices.value.slice(0, perPage.value))

// A contact's display name: company name, else person name, else email. Same
// precedence as the contacts/projects lists.
function contactDisplayName(c: Contact): string {
  const org = c.organisation_name?.trim()
  if (org) return org
  const person = `${c.first_name ?? ''} ${c.last_name ?? ''}`.trim()
  if (person) return person
  return c.email?.trim() || '(no name)'
}

function contactName(inv: Invoice): string {
  return contactNames.value.get(inv.contact_id) || '—'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    // Like the projects list, fetch contacts alongside and build an id → name map
    // for the Contact column. Both are org-scoped; a 401 on either is handled by
    // apiFetch (logout + redirect).
    const [invoiceList, contactList] = await Promise.all([listInvoices(), listContacts()])
    invoices.value = invoiceList
    contactNames.value = new Map(contactList.map((c) => [c.id, contactDisplayName(c)]))
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load invoices.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">Invoices</h1>
      <Button label="New Invoice" @click="newInvoice" />
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading invoices…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty -->
      <div v-else-if="invoices.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No invoices yet</p>
        <p class="text-sm text-fa-muted">Invoices you create will appear here.</p>
      </div>

      <!-- Data -->
      <template v-else>
        <div class="overflow-x-auto">
          <table class="w-full border-collapse text-sm">
            <thead>
              <tr>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Reference
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Contact
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Date
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                  Status
                </th>
                <th class="border-b border-fa-border px-4 py-3 text-right text-[13px] font-semibold text-fa-muted">
                  Total
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="inv in paged" :key="inv.id" class="group hover:bg-[#f7fafc]">
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <button
                    type="button"
                    class="text-left font-semibold text-fa-blue hover:underline"
                    @click="openDetail(inv.id)"
                  >
                    {{ inv.reference || '(no reference)' }}
                  </button>
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                  {{ contactName(inv) }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                  {{ formatDate(inv.dated_on) }}
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                  <span
                    class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
                    :class="invoiceStatusClass(inv.display_status)"
                    >{{ inv.display_status }}</span
                  >
                </td>
                <td class="border-b border-[#eef1f4] px-4 py-3.5 text-right align-middle tabular-nums">
                  {{ formatMoney(inv.total_value, inv.currency) }}
                </td>
              </tr>
            </tbody>
          </table>
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
