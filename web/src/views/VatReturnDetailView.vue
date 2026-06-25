<script setup lang="ts">
// VAT Return detail — the computed return for one period, modelled on FreeAgent's
// "VAT Return for period MM YY". Two tabs over GET /api/v1/vat/returns/:periodKey:
//   - Preview     → the 9-box card + the Net-VAT highlight + Payments to HMRC.
//   - Full Report → the Sales / Purchases line tables (the transactions behind the
//                   boxes).
// A right sidebar shows the VAT period, deadlines, and calculation details. The
// figures are read-only; "Mark as filed" snapshots the return and LOCKS the period
// (records dated inside it can no longer be changed). Online HMRC filing is a later slice.
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, RouterLink } from 'vue-router'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import { useAuthStore } from '@/stores/auth'
import { getVatReturn, markVatReturnFiled } from '@/services/vat.service'
import { formatMoney, formatDate } from '@/lib/format'
import { vatStatusClass } from '@/lib/vatStatus'
import type { VatReturn } from '@/types/vat'
import type { ApiError } from '@/lib/api'

const route = useRoute()
const auth = useAuthStore()
const periodKey = computed(() => String(route.params.periodKey ?? ''))

const ret = ref<VatReturn | null>(null)
const loading = ref(true)
const error = ref('')
const tab = ref<'preview' | 'full'>('preview')

// Filing. "Mark as filed" is owner/admin-only and only offered while the return is
// not already filed (a filed return shows no button — the period is locked).
const filing = ref(false)
const fileError = ref('')
const filedStatuses = ['Marked as filed', 'Filed', 'Pending']
const canFile = computed(
  () => auth.isOrgAdmin && !!ret.value && !filedStatuses.includes(ret.value.display_status),
)
const isFiled = computed(
  () => !!ret.value && filedStatuses.includes(ret.value.display_status),
)

async function markFiled() {
  if (filing.value || !ret.value) return
  fileError.value = ''
  filing.value = true
  try {
    ret.value = await markVatReturnFiled(periodKey.value)
  } catch (err) {
    fileError.value = (err as ApiError)?.message ?? 'Could not mark this return as filed.'
  } finally {
    filing.value = false
  }
}

// The 9 boxes in display order, with their official HMRC descriptions.
const boxes = computed(() => {
  const r = ret.value
  if (!r) return []
  return [
    { n: 1, label: 'VAT due on sales and other outputs', value: r.box1_vat_due_sales },
    {
      n: 2,
      label:
        'VAT due on intra-community acquisitions of goods made in Northern Ireland from EU Member States',
      value: r.box2_vat_due_acquisitions,
    },
    { n: 3, label: 'Total VAT due (the sum of boxes 1 and 2)', value: r.box3_total_vat_due },
    {
      n: 4,
      label: 'VAT reclaimed on purchases and other inputs (including acquisitions from the EU)',
      value: r.box4_vat_reclaimed,
    },
    {
      n: 5,
      label: 'Net VAT to be paid to Customs or reclaimed by you (difference between boxes 3 and 4)',
      value: r.box5_net_vat,
      highlight: true,
    },
    {
      n: 6,
      label: 'Total value of sales and all other outputs excluding any VAT',
      value: r.box6_total_sales_ex_vat,
    },
    {
      n: 7,
      label: 'Total value of purchases and all other inputs excluding any VAT',
      value: r.box7_total_purchases_ex_vat,
    },
    {
      n: 8,
      label:
        'Total value of intra-community dispatches of goods and related costs, excluding any VAT, from Northern Ireland to EU Member States',
      value: r.box8_ec_dispatches_ex_vat,
    },
    {
      n: 9,
      label:
        'Total value of intra-community acquisitions of goods and related costs, excluding any VAT, made in Northern Ireland from EU Member States',
      value: r.box9_ec_acquisitions_ex_vat,
    },
  ]
})

const basisLabel = computed(() => (ret.value?.accounting_basis === 'cash' ? 'Cash basis' : 'Invoice'))
const salesLines = computed(() => ret.value?.sales_lines ?? [])
const purchaseLines = computed(() => ret.value?.purchase_lines ?? [])

async function load() {
  loading.value = true
  error.value = ''
  try {
    ret.value = await getVatReturn(periodKey.value)
  } catch (err) {
    error.value = (err as ApiError)?.message ?? 'Could not load this VAT return.'
  } finally {
    loading.value = false
  }
}

watch(periodKey, load)
onMounted(load)
</script>

<template>
  <AppLayout>
    <!-- Header -->
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-3">
        <RouterLink to="/vat-returns" class="text-sm font-semibold text-fa-blue hover:underline">
          ← VAT Returns
        </RouterLink>
        <h1 v-if="ret" class="text-[22px] font-bold">VAT Return for period {{ ret.label }}</h1>
      </div>
      <div v-if="ret" class="flex items-center gap-3">
        <span
          class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
          :class="vatStatusClass(ret.display_status)"
          >{{ ret.display_status }}</span
        >
        <Button v-if="canFile" label="Mark as filed" :loading="filing" @click="markFiled" />
      </div>
    </div>

    <div
      v-if="fileError"
      class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
      role="alert"
    >
      {{ fileError }}
    </div>

    <!-- Loading / error -->
    <FaCard v-if="loading" title="VAT Return">
      <div class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
    </FaCard>
    <FaCard v-else-if="error" title="VAT Return">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ error }}</p>
        <Button label="Try again" severity="secondary" outlined @click="load" />
      </div>
    </FaCard>

    <template v-else-if="ret">
      <!-- Tabs -->
      <div class="mb-5 flex gap-6 border-b border-fa-border">
        <button
          type="button"
          class="-mb-px border-b-2 px-1 py-2 text-sm font-semibold"
          :class="tab === 'preview' ? 'border-fa-blue text-fa-text' : 'border-transparent text-fa-muted hover:text-fa-text'"
          @click="tab = 'preview'"
        >
          Preview
        </button>
        <button
          type="button"
          class="-mb-px border-b-2 px-1 py-2 text-sm font-semibold"
          :class="tab === 'full' ? 'border-fa-blue text-fa-text' : 'border-transparent text-fa-muted hover:text-fa-text'"
          @click="tab = 'full'"
        >
          Full Report
        </button>
      </div>

      <div class="grid items-start gap-5 lg:grid-cols-[minmax(0,1fr)_300px]">
        <!-- ============ MAIN ============ -->
        <div>
          <!-- ---------- PREVIEW ---------- -->
          <template v-if="tab === 'preview'">
            <div
              v-if="isFiled"
              class="mb-4 rounded border border-[#cfe9c7] bg-[#eaf7e6] px-3 py-2 text-sm text-[#3f8038]"
              role="note"
            >
              This return is marked as filed — the transactions in this period are now locked and can
              no longer be changed.
            </div>
            <div
              v-else
              class="mb-4 rounded border border-[#f3dca8] bg-[#fef8ec] px-3 py-2 text-sm text-[#8a6d3b]"
              role="note"
            >
              Online filing to HMRC (Making Tax Digital) is coming soon — for now, review the return
              and use “Mark as filed” once you've submitted it. Filing locks the period's records.
            </div>

            <!-- The 9-box card -->
            <div class="overflow-hidden rounded-[6px] border border-fa-border bg-white">
              <div class="bg-fa-green px-4 py-3 text-white">
                <div class="text-lg font-bold">VAT Return</div>
                <div class="text-xs opacity-90">
                  {{ formatDate(ret.start_date) }} to {{ formatDate(ret.end_date) }}
                </div>
              </div>
              <div>
                <div
                  v-for="b in boxes"
                  :key="b.n"
                  class="flex items-center gap-3 border-b border-[#eef1f4] px-4 py-2.5 last:border-b-0"
                  :class="b.highlight ? 'bg-[#eaf7e6]' : ''"
                >
                  <div class="flex-1 text-[13px] leading-snug text-fa-text">{{ b.label }}</div>
                  <div
                    class="flex h-6 w-6 shrink-0 items-center justify-center rounded bg-fa-green text-xs font-bold text-white"
                  >
                    {{ b.n }}
                  </div>
                  <div class="w-28 shrink-0 text-right text-sm font-semibold tabular-nums">
                    {{ formatMoney(b.value) }}
                  </div>
                </div>
              </div>
            </div>

            <!-- Payments to HMRC -->
            <FaCard title="Payments to HMRC" class="mt-5">
              <div class="flex items-center justify-between py-1">
                <div class="flex items-center gap-3">
                  <span
                    class="inline-flex h-10 w-12 flex-col items-center justify-center rounded bg-fa-card-header text-[10px] font-semibold uppercase text-fa-muted"
                  >
                    <span class="text-sm leading-none text-fa-text">{{
                      formatDate(ret.due_on).split(' ')[0]
                    }}</span>
                    <span>{{ formatDate(ret.due_on).split(' ')[1] }}</span>
                  </span>
                  <span class="text-sm font-semibold">{{
                    ret.is_reclaim ? 'Refund Due' : 'Payment Due'
                  }}</span>
                </div>
                <span class="text-sm font-semibold tabular-nums">{{ formatMoney(ret.net_due) }}</span>
              </div>
            </FaCard>
          </template>

          <!-- ---------- FULL REPORT ---------- -->
          <template v-else>
            <!-- Sales -->
            <h2 class="mb-2 text-[15px] font-bold">Sales</h2>
            <div class="mb-6 overflow-hidden rounded-[5px] border border-fa-border bg-white">
              <div class="overflow-x-auto">
                <table class="w-full border-collapse text-sm">
                  <thead>
                    <tr class="text-[13px] font-semibold text-fa-muted">
                      <th class="border-b border-fa-border px-4 py-2.5 text-left">Date</th>
                      <th class="border-b border-fa-border px-4 py-2.5 text-left">Description</th>
                      <th class="border-b border-fa-border px-4 py-2.5 text-right">Box 1 (VAT)</th>
                      <th class="border-b border-fa-border px-4 py-2.5 text-right">Box 6 (Net)</th>
                    </tr>
                    <tr class="bg-fa-card-header text-[13px] font-semibold">
                      <td class="border-b border-fa-border px-4 py-2" colspan="2">Totals</td>
                      <td class="border-b border-fa-border px-4 py-2 text-right tabular-nums">
                        {{ formatMoney(ret.box1_vat_due_sales) }}
                      </td>
                      <td class="border-b border-fa-border px-4 py-2 text-right tabular-nums">
                        {{ formatMoney(ret.box6_total_sales_ex_vat) }}
                      </td>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(l, i) in salesLines" :key="i" class="hover:bg-[#f7fafc]">
                      <td class="border-b border-[#eef1f4] px-4 py-2.5 text-fa-muted">
                        {{ formatDate(l.date) }}
                      </td>
                      <td class="border-b border-[#eef1f4] px-4 py-2.5">{{ l.description }}</td>
                      <td class="border-b border-[#eef1f4] px-4 py-2.5 text-right tabular-nums">
                        {{ formatMoney(l.vat) }}
                      </td>
                      <td class="border-b border-[#eef1f4] px-4 py-2.5 text-right tabular-nums">
                        {{ formatMoney(l.net) }}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div v-if="salesLines.length === 0" class="px-4 py-6 text-center text-sm text-fa-muted">
                No sales in this period.
              </div>
            </div>

            <!-- Purchases -->
            <h2 class="mb-2 text-[15px] font-bold">Purchases</h2>
            <div class="overflow-hidden rounded-[5px] border border-fa-border bg-white">
              <div class="overflow-x-auto">
                <table class="w-full border-collapse text-sm">
                  <thead>
                    <tr class="text-[13px] font-semibold text-fa-muted">
                      <th class="border-b border-fa-border px-4 py-2.5 text-left">Date</th>
                      <th class="border-b border-fa-border px-4 py-2.5 text-left">Description</th>
                      <th class="border-b border-fa-border px-4 py-2.5 text-right">Box 4 (VAT)</th>
                      <th class="border-b border-fa-border px-4 py-2.5 text-right">Box 7 (Net)</th>
                    </tr>
                    <tr class="bg-fa-card-header text-[13px] font-semibold">
                      <td class="border-b border-fa-border px-4 py-2" colspan="2">Totals</td>
                      <td class="border-b border-fa-border px-4 py-2 text-right tabular-nums">
                        {{ formatMoney(ret.box4_vat_reclaimed) }}
                      </td>
                      <td class="border-b border-fa-border px-4 py-2 text-right tabular-nums">
                        {{ formatMoney(ret.box7_total_purchases_ex_vat) }}
                      </td>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(l, i) in purchaseLines" :key="i" class="hover:bg-[#f7fafc]">
                      <td class="border-b border-[#eef1f4] px-4 py-2.5 text-fa-muted">
                        {{ formatDate(l.date) }}
                      </td>
                      <td class="border-b border-[#eef1f4] px-4 py-2.5">{{ l.description }}</td>
                      <td class="border-b border-[#eef1f4] px-4 py-2.5 text-right tabular-nums">
                        {{ formatMoney(l.vat) }}
                      </td>
                      <td class="border-b border-[#eef1f4] px-4 py-2.5 text-right tabular-nums">
                        {{ formatMoney(l.net) }}
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div
                v-if="purchaseLines.length === 0"
                class="px-4 py-6 text-center text-sm text-fa-muted"
              >
                No purchases in this period.
              </div>
            </div>
          </template>
        </div>

        <!-- ============ SIDEBAR ============ -->
        <aside class="flex flex-col gap-4">
          <FaCard title="VAT period">
            <div class="mb-3">
              <span
                class="inline-block rounded-full border px-2.5 py-0.5 text-xs font-semibold tracking-wide"
                :class="vatStatusClass(ret.display_status)"
                >{{ ret.display_status }}</span
              >
            </div>
            <div class="grid grid-cols-2 gap-3 text-sm">
              <div>
                <div class="text-xs text-fa-muted">Start</div>
                <div class="font-semibold">{{ formatDate(ret.start_date) }}</div>
              </div>
              <div>
                <div class="text-xs text-fa-muted">End</div>
                <div class="font-semibold">{{ formatDate(ret.end_date) }}</div>
              </div>
            </div>
          </FaCard>

          <FaCard title="Important deadlines">
            <div class="grid grid-cols-2 gap-3 text-sm">
              <div>
                <div class="text-xs text-fa-muted">File by</div>
                <div class="font-semibold">{{ formatDate(ret.due_on) }}</div>
              </div>
              <div>
                <div class="text-xs text-fa-muted">Pay by</div>
                <div class="font-semibold">{{ formatDate(ret.due_on) }}</div>
              </div>
            </div>
          </FaCard>

          <FaCard title="Calculation details">
            <dl class="space-y-3 text-sm">
              <div>
                <dt class="text-xs text-fa-muted">Scheme</dt>
                <dd class="font-semibold">Standard Scheme</dd>
              </div>
              <div>
                <dt class="text-xs text-fa-muted">Calculation Basis</dt>
                <dd class="font-semibold">{{ basisLabel }}</dd>
              </div>
              <div>
                <dt class="text-xs text-fa-muted">Fuel Scale Charge</dt>
                <dd class="font-semibold">None</dd>
              </div>
            </dl>
          </FaCard>
        </aside>
      </div>
    </template>
  </AppLayout>
</template>
