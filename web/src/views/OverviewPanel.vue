<script setup lang="ts">
// Overview tab of the Overview page (web/src/views/OverviewDashboardView.vue).
//
// The new FreeAgent-style financial dashboard. Real cards so far:
//   - CASHFLOW         — money in vs money out per month (last 12 months) + totals.
//   - INVOICE TIMELINE — SENT invoices' totals per month, stacked Overdue/Due/Paid,
//                        + the Outstanding figure + New invoice / View all links.
//   - BANKING          — the org's total bank balance + a balance-over-time area
//                        chart (last 12 months) + Add account / View all links.
// Each loads independently (its own loading/error) so one failing read never blanks
// the others. (The Expenses & Bills card is deferred — see BACKLOG.md.)
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Chart from 'primevue/chart'
import Button from 'primevue/button'
import FaCard from '@/components/FaCard.vue'
import { formatMoney } from '@/lib/format'
import type { ApiError } from '@/lib/api'
import { getCashflow, getInvoiceTimeline, getBanking } from '@/services/overview.service'
import type { Cashflow, InvoiceTimeline, Banking } from '@/types/overview'

const router = useRouter()

// Shared palette — brand green / amber / red / the app's blue.
const GREEN = '#2d6a4f'
const RED = '#c0392b'
const AMBER = '#e0a93b'
const BLUE = '#1f6fd0'

// Short month label ("Jul") from a bucket's ISO first-of-month date.
function shortMonth(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString('en-GB', { month: 'short' })
}

// A £-formatting tooltip label callback, shared by the charts. The pound strings
// are parsed to numbers ONLY for plotting; the figures shown to the user come from
// the API strings via formatMoney (no money maths here).
function moneyTooltip(ctx: { dataset: { label?: string }; parsed: { y: number } }): string {
  return ` ${ctx.dataset.label}: ${formatMoney(String(ctx.parsed.y))}`
}

// Compact £ y-axis tick, e.g. "£50,000".
function poundAxis(v: number | string): string {
  return '£' + Number(v).toLocaleString('en-GB')
}

// ---- Cashflow ----------------------------------------------------------------
const cashflow = ref<Cashflow | null>(null)
const loading = ref(true)
const error = ref('')

// Balance is the API's net (incoming − outgoing); green when in the black.
const balancePositive = computed(() => Number(cashflow.value?.balance ?? 0) >= 0)

const chartData = computed(() => {
  const months = cashflow.value?.months ?? []
  return {
    labels: months.map((m) => shortMonth(m.month)),
    datasets: [
      { label: 'Incoming', backgroundColor: GREEN, borderRadius: 3, data: months.map((m) => Number(m.incoming)) },
      { label: 'Outgoing', backgroundColor: RED, borderRadius: 3, data: months.map((m) => Number(m.outgoing)) },
    ],
  }
})

const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      position: 'top' as const,
      align: 'end' as const,
      labels: { boxWidth: 12, boxHeight: 12, font: { size: 12 }, color: '#586069' },
    },
    tooltip: { callbacks: { label: moneyTooltip } },
  },
  scales: {
    x: { grid: { display: false }, ticks: { font: { size: 12 }, color: '#586069' } },
    y: {
      beginAtZero: true,
      grid: { color: '#eef1f4' },
      ticks: { font: { size: 12 }, color: '#586069', callback: poundAxis },
    },
  },
}

// ---- Invoice Timeline --------------------------------------------------------
const timeline = ref<InvoiceTimeline | null>(null)
const timelineLoading = ref(true)
const timelineError = ref('')

const timelineChartData = computed(() => {
  const months = timeline.value?.months ?? []
  return {
    labels: months.map((m) => shortMonth(m.month)),
    datasets: [
      { label: 'Overdue', backgroundColor: RED, data: months.map((m) => Number(m.overdue)) },
      { label: 'Due', backgroundColor: AMBER, data: months.map((m) => Number(m.due)) },
      { label: 'Paid', backgroundColor: GREEN, data: months.map((m) => Number(m.paid)) },
    ],
  }
})

// Same as the cashflow options but STACKED (the three status series stack per month).
const timelineChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      position: 'top' as const,
      align: 'end' as const,
      labels: { boxWidth: 12, boxHeight: 12, font: { size: 12 }, color: '#586069' },
    },
    tooltip: { callbacks: { label: moneyTooltip } },
  },
  scales: {
    x: { stacked: true, grid: { display: false }, ticks: { font: { size: 12 }, color: '#586069' } },
    y: {
      stacked: true,
      beginAtZero: true,
      grid: { color: '#eef1f4' },
      ticks: { font: { size: 12 }, color: '#586069', callback: poundAxis },
    },
  },
}

// ---- Banking -----------------------------------------------------------------
const banking = ref<Banking | null>(null)
const bankingLoading = ref(true)
const bankingError = ref('')

// Red when the org is overdrawn (negative total balance).
const bankingNegative = computed(() => Number(banking.value?.balance ?? 0) < 0)

// A filled line (area) of the month-end total balance.
const bankingChartData = computed(() => {
  const months = banking.value?.months ?? []
  return {
    labels: months.map((m) => shortMonth(m.month)),
    datasets: [
      {
        label: 'Balance',
        data: months.map((m) => Number(m.balance)),
        borderColor: BLUE,
        backgroundColor: 'rgba(31,111,208,0.12)',
        borderWidth: 2,
        fill: true,
        tension: 0.35,
        pointRadius: 2,
        pointBackgroundColor: BLUE,
      },
    ],
  }
})

// Single series, so no legend; y auto-scales to the balance range (it can be negative).
const bankingChartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false },
    tooltip: { callbacks: { label: moneyTooltip } },
  },
  scales: {
    x: { grid: { display: false }, ticks: { font: { size: 12 }, color: '#586069' } },
    y: {
      grid: { color: '#eef1f4' },
      ticks: { font: { size: 12 }, color: '#586069', callback: poundAxis },
    },
  },
}

onMounted(() => {
  // Three independent reads so a failure in one card doesn't blank the others.
  getCashflow()
    .then((c) => (cashflow.value = c))
    .catch((e) => (error.value = (e as ApiError)?.message ?? 'Could not load your cashflow.'))
    .finally(() => (loading.value = false))

  getInvoiceTimeline()
    .then((t) => (timeline.value = t))
    .catch((e) => (timelineError.value = (e as ApiError)?.message ?? 'Could not load your invoices.'))
    .finally(() => (timelineLoading.value = false))

  getBanking()
    .then((b) => (banking.value = b))
    .catch((e) => (bankingError.value = (e as ApiError)?.message ?? 'Could not load your banking.'))
    .finally(() => (bankingLoading.value = false))
})
</script>

<template>
  <div>
    <!-- Cashflow (full width) -->
    <FaCard title="Cashflow" note="Last 12 months">
      <div v-if="loading" class="py-14 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading your cashflow…
      </div>
      <div v-else-if="error" class="py-10 text-center text-sm text-[#c0392b]">{{ error }}</div>
      <div v-else-if="cashflow" class="flex flex-col gap-6 lg:flex-row lg:items-stretch">
        <!-- Chart -->
        <div class="min-w-0 flex-1">
          <div class="relative h-[260px]">
            <Chart type="bar" :data="chartData" :options="chartOptions" class="h-full w-full" />
          </div>
        </div>

        <!-- Totals -->
        <div
          class="flex shrink-0 flex-row justify-between gap-4 border-t border-fa-border pt-4 lg:w-44 lg:flex-col lg:justify-center lg:border-l lg:border-t-0 lg:pl-6 lg:pt-0"
        >
          <div>
            <div class="text-[13px] text-fa-muted">Incoming</div>
            <div class="text-[22px] font-bold leading-tight">{{ formatMoney(cashflow.incoming) }}</div>
          </div>
          <div>
            <div class="text-[13px] text-fa-muted">Outgoing</div>
            <div class="text-[22px] font-bold leading-tight">{{ formatMoney(cashflow.outgoing) }}</div>
          </div>
          <div>
            <div class="text-[13px] text-fa-muted">Balance</div>
            <div
              class="text-[22px] font-bold leading-tight"
              :class="balancePositive ? 'text-[#3f8038]' : 'text-[#c0392b]'"
            >
              {{ formatMoney(cashflow.balance) }}
            </div>
          </div>
        </div>
      </div>
    </FaCard>

    <!-- Second row: Invoice Timeline + Banking (real) + the remaining placeholder -->
    <div class="grid gap-4 sm:grid-cols-2">
      <!-- Invoice Timeline -->
      <FaCard title="Invoice Timeline">
        <div v-if="timelineLoading" class="py-14 text-center text-fa-muted">
          <i class="pi pi-spin pi-spinner mr-2" />Loading your invoices…
        </div>
        <div v-else-if="timelineError" class="py-10 text-center text-sm text-[#c0392b]">
          {{ timelineError }}
        </div>
        <div v-else-if="timeline">
          <div class="relative h-[210px]">
            <Chart type="bar" :data="timelineChartData" :options="timelineChartOptions" class="h-full w-full" />
          </div>
          <div class="mt-2 text-right text-[13px] text-fa-muted">
            Outstanding
            <span class="ml-1 text-[15px] font-bold text-fa-text">{{ formatMoney(timeline.outstanding) }}</span>
          </div>
          <div class="mt-3 flex items-center justify-between border-t border-fa-border pt-3">
            <Button label="New invoice" size="small" severity="secondary" outlined @click="router.push('/invoices/new')" />
            <RouterLink to="/invoices" class="text-sm font-semibold text-fa-blue hover:underline">
              View all invoices
            </RouterLink>
          </div>
        </div>
      </FaCard>

      <!-- Banking -->
      <FaCard title="Banking">
        <div v-if="bankingLoading" class="py-14 text-center text-fa-muted">
          <i class="pi pi-spin pi-spinner mr-2" />Loading your banking…
        </div>
        <div v-else-if="bankingError" class="py-10 text-center text-sm text-[#c0392b]">
          {{ bankingError }}
        </div>
        <div v-else-if="banking">
          <div class="mb-3 flex items-start justify-between">
            <span class="text-[13px] text-fa-muted">All accounts</span>
            <div class="text-right">
              <div
                class="text-[22px] font-bold leading-tight"
                :class="bankingNegative ? 'text-[#c0392b]' : 'text-fa-text'"
              >
                {{ formatMoney(banking.balance) }}
              </div>
              <div class="text-[13px] text-fa-muted">Balance</div>
            </div>
          </div>
          <div class="relative h-[180px]">
            <Chart type="line" :data="bankingChartData" :options="bankingChartOptions" class="h-full w-full" />
          </div>
          <div class="mt-3 flex items-center justify-between border-t border-fa-border pt-3">
            <Button label="Add account" size="small" severity="secondary" outlined @click="router.push('/bank-accounts/new')" />
            <RouterLink to="/bank-accounts" class="text-sm font-semibold text-fa-blue hover:underline">
              View all bank accounts
            </RouterLink>
          </div>
        </div>
      </FaCard>
    </div>
  </div>
</template>
