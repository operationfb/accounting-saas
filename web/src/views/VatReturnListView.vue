<script setup lang="ts">
// VAT Returns list — wired to GET /api/v1/vat/periods. Mirrors BillListView /
// InvoiceListView: AppLayout wrapper, a hand-rolled Tailwind table with the fa-*
// theme colours, and a loading/error/empty/data state machine.
//
// The periods are GENERATED from the org's VAT registration settings (effective
// date / first-return end / frequency), newest-first. When the org isn't registered
// yet the list is empty and we point the user at the VAT Registration screen.
//
// Rows are not yet clickable: the per-period return detail (Preview / Full Report)
// is the next slice — the period label becomes a link to /vat-returns/:periodKey then.
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import { listVatPeriods } from '@/services/vat.service'
import { formatDate } from '@/lib/format'
import { vatStatusClass } from '@/lib/vatStatus'
import type { VatPeriod } from '@/types/vat'
import type { ApiError } from '@/lib/api'

const periods = ref<VatPeriod[]>([])
const loading = ref(true)
const error = ref('')

const router = useRouter()

function goToSettings() {
  router.push('/vat-registration')
}

function openReturn(periodKey: string) {
  router.push(`/vat-returns/${encodeURIComponent(periodKey)}`)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    periods.value = await listVatPeriods()
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load VAT returns.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">VAT Returns</h1>
    </div>

    <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
      <!-- Loading -->
      <div v-if="loading" class="px-4 py-12 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading VAT returns…
      </div>

      <!-- Error -->
      <div v-else-if="error" class="px-4 py-12 text-center">
        <p class="mb-3 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Retry" severity="secondary" outlined @click="load" />
      </div>

      <!-- Empty (not registered yet, or settings incomplete) -->
      <div v-else-if="periods.length === 0" class="px-4 py-14 text-center">
        <p class="mb-1 font-semibold">No VAT returns yet</p>
        <p class="mb-4 text-sm text-fa-muted">
          Add your VAT registration details — effective date, first return period end and
          frequency — and your return periods will appear here.
        </p>
        <Button label="Go to VAT Registration" severity="secondary" outlined @click="goToSettings" />
      </div>

      <!-- Data -->
      <div v-else class="overflow-x-auto">
        <table class="w-full border-collapse text-sm">
          <thead>
            <tr>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Period
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                From
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                To
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Filing due
              </th>
              <th class="border-b border-fa-border px-4 py-3 text-left text-[13px] font-semibold text-fa-muted">
                Status
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in periods" :key="p.period_key" class="group hover:bg-[#f7fafc]">
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <button
                  type="button"
                  class="text-left font-semibold text-fa-blue hover:underline"
                  @click="openReturn(p.period_key)"
                >
                  {{ p.label }}
                </button>
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ formatDate(p.start_date) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ formatDate(p.end_date) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle text-fa-muted">
                {{ formatDate(p.due_on) }}
              </td>
              <td class="border-b border-[#eef1f4] px-4 py-3.5 align-middle">
                <span
                  class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
                  :class="vatStatusClass(p.display_status)"
                  >{{ p.display_status }}</span
                >
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </AppLayout>
</template>
