<script setup lang="ts">
// VAT dashboard — the VAT tab of the Overview page
// (web/src/views/OverviewDashboardView.vue). The read layer over HMRC's MTD VAT
// account. Extracted verbatim from the old VatDashboardView so its look & feel is
// unchanged; the only difference is it no longer wraps AppLayout (the Overview
// container provides the single page layout) — it renders inside the tab.
//
// Mirrors the approved mockup: a compliance hero (current return + deadline + box
// figures), the return periods (HMRC obligations), what you owe (liabilities),
// payments to HMRC, penalties & points, and the registered business details.
//
// Three states: a connect gate when HMRC isn't linked yet, a "register first" prompt
// when there's no VRN, and the populated account when connected. The HMRC reads load
// with Promise.allSettled so one slow/failing/empty resource never blanks the page —
// each card carries its own error. Hero box figures come from our OWN computed
// return for the open period (best-effort; a period drift just hides them).
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import Button from 'primevue/button'
import FaCard from '@/components/FaCard.vue'
import { formatMoney, formatDate } from '@/lib/format'
import { prewarmFraudSignals } from '@/lib/fraudSignals'
import type { ApiError } from '@/lib/api'
import {
  getVatSettings,
  getVatReturn,
  getHmrcObligations,
  getHmrcLiabilities,
  getHmrcPayments,
  getHmrcPenalties,
  getHmrcInformation,
} from '@/services/vat.service'
import { getHmrcConnectUrl } from '@/services/integrations.service'
import type {
  VatSettings,
  VatReturn,
  HmrcObligation,
  HmrcLiability,
  HmrcPayment,
  HmrcPenalties,
  HmrcInformation,
} from '@/types/vat'

const router = useRouter()

const loading = ref(true)
const settings = ref<VatSettings | null>(null)

const connected = computed(() => !!settings.value?.hmrc_connected)
const registered = computed(() => !!settings.value?.vat_registered && !!settings.value?.vrn)

// Per-card data + independent error strings (Promise.allSettled fills these), so one
// failing HMRC read leaves the other cards intact.
const obligations = ref<HmrcObligation[]>([])
const liabilities = ref<HmrcLiability[]>([])
const payments = ref<HmrcPayment[]>([])
const penalties = ref<HmrcPenalties | null>(null)
const information = ref<HmrcInformation | null>(null)
const heroReturn = ref<VatReturn | null>(null)
const cardErr = ref<Record<string, string>>({})

const connecting = ref(false)
const connectError = ref('')

const openObligation = computed(() => obligations.value.find((o) => o.status === 'O') ?? null)

const pillBase =
  'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-xs font-semibold'
const greenPill = 'bg-[#eaf7e6] text-[#3f8038] border-[#cfe9c7]'
const amberPill = 'bg-[#fdf6e3] text-[#8a6d3b] border-[#f0e0b6]'
const redPill = 'bg-[#fdecec] text-[#c0392b] border-[#f6d3d0]'
const bluePill = 'bg-[#e8f1fb] text-[#1f6fd0] border-[#cfe2f7]'

function msg(e: unknown): string {
  return (e as ApiError)?.message ?? 'Could not load from HMRC.'
}

// Sum 2dp pound strings in integer pence (no float drift), then render back.
function sumPounds(values: string[]): string {
  let pence = 0
  for (const v of values) {
    const n = Number(v)
    if (!Number.isNaN(n)) pence += Math.round(n * 100)
  }
  const sign = pence < 0 ? '-' : ''
  const abs = Math.abs(pence)
  return `${sign}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`
}

const totalOutstanding = computed(() =>
  sumPounds(liabilities.value.map((l) => l.outstanding_amount)),
)
const totalPaid = computed(() => sumPounds(payments.value.map((p) => p.amount)))

function daysUntil(iso: string): number {
  const due = new Date(iso + 'T00:00:00')
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  return Math.round((due.getTime() - today.getTime()) / 86_400_000)
}

// Deadline countdown chip for the open obligation.
const deadline = computed(() => {
  const open = openObligation.value
  if (!open) return null
  const days = daysUntil(open.due)
  if (days < 0)
    return { days, label: `Overdue by ${Math.abs(days)} day${Math.abs(days) === 1 ? '' : 's'}`, cls: redPill }
  if (days <= 14) return { days, label: `${days} day${days === 1 ? '' : 's'} to deadline`, cls: amberPill }
  return { days, label: `${days} days to deadline`, cls: greenPill }
})

// Headline status pill: on track / due soon / overdue (or none).
const statusPill = computed(() => {
  const d = deadline.value
  if (!d) return { label: 'No open return', cls: bluePill }
  if (d.days < 0) return { label: 'Overdue', cls: redPill }
  if (d.days <= 14) return { label: 'Due soon', cls: amberPill }
  return { label: 'On track', cls: greenPill }
})

// The late-submission points meter: `threshold` pips, the first `active_points` filled.
const pointsPips = computed(() => {
  const p = penalties.value
  if (!p) return []
  const total = Math.max(p.threshold, p.active_points)
  return Array.from({ length: total }, (_, i) => i < p.active_points)
})

const noPenalties = computed(
  () => !!penalties.value && (penalties.value.penalties?.length ?? 0) === 0 && penalties.value.active_points === 0,
)

const registrationAddress = computed(() => {
  const inf = information.value
  if (!inf) return ''
  return [...(inf.address_lines ?? []), inf.postcode].filter(Boolean).join(', ')
})

function openReturn(periodEnd: string) {
  router.push(`/vat-returns/${encodeURIComponent(periodEnd)}`)
}

async function load() {
  loading.value = true
  cardErr.value = {}
  try {
    settings.value = await getVatSettings()
  } catch (e) {
    cardErr.value.settings = msg(e)
  }
  if (settings.value && connected.value) {
    await loadAccount()
  }
  loading.value = false
}

async function loadAccount() {
  const [ob, li, pa, pe, inf] = await Promise.allSettled([
    getHmrcObligations(),
    getHmrcLiabilities(),
    getHmrcPayments(),
    getHmrcPenalties(),
    getHmrcInformation(),
  ])
  if (ob.status === 'fulfilled') obligations.value = ob.value
  else cardErr.value.obligations = msg(ob.reason)
  if (li.status === 'fulfilled') liabilities.value = li.value
  else cardErr.value.liabilities = msg(li.reason)
  if (pa.status === 'fulfilled') payments.value = pa.value
  else cardErr.value.payments = msg(pa.reason)
  if (pe.status === 'fulfilled') penalties.value = pe.value
  else cardErr.value.penalties = msg(pe.reason)
  if (inf.status === 'fulfilled') information.value = inf.value
  else cardErr.value.information = msg(inf.reason)

  // Hero figures from our own computed return for the open period (best-effort).
  const open = openObligation.value
  if (open) {
    try {
      heroReturn.value = await getVatReturn(open.end)
    } catch {
      heroReturn.value = null
    }
  }
}

async function connect() {
  connecting.value = true
  connectError.value = ''
  try {
    const url = await getHmrcConnectUrl()
    window.location.assign(url)
  } catch (e) {
    connectError.value = msg(e)
    connecting.value = false
  }
}

onMounted(() => {
  // Pre-warm the HMRC fraud-prevention signals (the slow WebRTC local-IP gather)
  // while the dashboard loads, so the live HMRC reads don't wait on it.
  prewarmFraudSignals()
  load()
})
</script>

<template>
  <div>
    <!-- Header -->
    <div class="mb-[18px] flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="text-[22px] font-bold">VAT dashboard</h1>
        <p v-if="settings?.vrn" class="mt-0.5 text-[13px] text-fa-muted">
          VRN {{ settings.vrn }}
          <span v-if="settings.return_frequency"> · {{ settings.return_frequency }} returns</span>
        </p>
      </div>
      <div v-if="connected" class="flex items-center gap-2">
        <span :class="[pillBase, greenPill]">
          <span class="h-2 w-2 rounded-full bg-[#3f8038]" />Connected to HMRC
        </span>
        <Button
          icon="pi pi-refresh"
          severity="secondary"
          outlined
          :loading="loading"
          aria-label="Refresh from HMRC"
          @click="load"
        />
      </div>
    </div>

    <!-- Loading -->
    <div
      v-if="loading"
      class="rounded-[5px] border border-fa-border bg-white px-4 py-14 text-center text-fa-muted"
    >
      <i class="pi pi-spin pi-spinner mr-2" />Loading your VAT account…
    </div>

    <!-- Register first -->
    <div
      v-else-if="!registered"
      class="rounded-[5px] border border-fa-border bg-white px-4 py-14 text-center"
    >
      <p class="mb-1 font-semibold">Add your VAT registration first</p>
      <p class="mx-auto mb-4 max-w-md text-sm text-fa-muted">
        Enter your VRN and return details, then connect to HMRC to see your VAT account here.
      </p>
      <Button
        label="Go to VAT Registration"
        severity="secondary"
        outlined
        @click="router.push('/vat-registration')"
      />
    </div>

    <!-- Connect gate -->
    <div
      v-else-if="!connected"
      class="rounded-[5px] border border-fa-border bg-white px-4 py-14 text-center"
    >
      <i class="pi pi-link mb-3 block text-2xl text-fa-muted" />
      <p class="mb-1 font-semibold">Connect to HMRC</p>
      <p class="mx-auto mb-4 max-w-md text-sm text-fa-muted">
        Link your HMRC Making Tax Digital account to see what's due, what you owe, your payments and
        any penalties — all in one place.
      </p>
      <Button label="Connect to HMRC" :loading="connecting" @click="connect" />
      <p v-if="connectError" class="mt-3 text-sm text-[#c0392b]">{{ connectError }}</p>
    </div>

    <!-- Connected dashboard -->
    <template v-else>
      <!-- Compliance hero -->
      <FaCard>
        <template #header>
          <div class="flex w-full flex-wrap items-center justify-between gap-2">
            <span class="text-[13px] text-fa-muted">Current return</span>
            <div class="flex items-center gap-2">
              <span v-if="openObligation" class="text-[15px] font-semibold">
                {{ formatDate(openObligation.start) }} – {{ formatDate(openObligation.end) }}
              </span>
              <span :class="[pillBase, statusPill.cls]">{{ statusPill.label }}</span>
            </div>
          </div>
        </template>

        <div v-if="!openObligation" class="text-sm text-fa-muted">
          No open VAT return right now — you're all caught up.
        </div>
        <template v-else>
          <div class="flex flex-wrap items-end justify-between gap-4">
            <div>
              <div class="text-[30px] font-bold leading-none">
                {{ heroReturn ? formatMoney(heroReturn.net_due) : '—' }}
              </div>
              <p class="mt-1 text-[13px] text-fa-muted">
                Net VAT {{ heroReturn?.is_reclaim ? 'reclaim' : 'due' }} · due
                {{ formatDate(openObligation.due) }}
              </p>
            </div>
            <span v-if="deadline" :class="[pillBase, deadline.cls]">
              <i class="pi pi-clock" />{{ deadline.label }}
            </span>
          </div>

          <div v-if="heroReturn" class="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div class="rounded-[5px] bg-fa-card-header px-3.5 py-3">
              <div class="text-[13px] text-fa-muted">Sales (box 6)</div>
              <div class="mt-0.5 text-xl font-bold">
                {{ formatMoney(heroReturn.box6_total_sales_ex_vat) }}
              </div>
            </div>
            <div class="rounded-[5px] bg-fa-card-header px-3.5 py-3">
              <div class="text-[13px] text-fa-muted">VAT on sales (box 1)</div>
              <div class="mt-0.5 text-xl font-bold">
                {{ formatMoney(heroReturn.box1_vat_due_sales) }}
              </div>
            </div>
            <div class="rounded-[5px] bg-fa-card-header px-3.5 py-3">
              <div class="text-[13px] text-fa-muted">VAT reclaimed (box 4)</div>
              <div class="mt-0.5 text-xl font-bold">
                {{ formatMoney(heroReturn.box4_vat_reclaimed) }}
              </div>
            </div>
          </div>

          <div class="mt-4">
            <Button label="Review & file return" icon="pi pi-send" @click="openReturn(openObligation.end)" />
          </div>
        </template>
      </FaCard>

      <!-- Return periods (HMRC obligations) -->
      <FaCard title="Return periods" note="From HMRC obligations">
        <div v-if="cardErr.obligations" class="text-sm text-[#c0392b]">{{ cardErr.obligations }}</div>
        <div v-else-if="obligations.length === 0" class="text-sm text-fa-muted">
          No obligations found for the last year.
        </div>
        <table v-else class="w-full border-collapse text-sm">
          <tbody>
            <tr
              v-for="o in obligations"
              :key="o.period_key"
              class="border-b border-[#eef1f4] last:border-0"
            >
              <td class="py-2.5 align-middle">
                <button
                  type="button"
                  class="text-left font-semibold text-fa-blue hover:underline"
                  @click="openReturn(o.end)"
                >
                  {{ formatDate(o.start) }} – {{ formatDate(o.end) }}
                </button>
                <div class="text-[12px] text-fa-muted">
                  {{
                    o.status === 'F' && o.received
                      ? 'Filed ' + formatDate(o.received)
                      : 'Due ' + formatDate(o.due)
                  }}
                </div>
              </td>
              <td class="py-2.5 text-right align-middle">
                <span :class="[pillBase, o.status === 'O' ? amberPill : greenPill]">
                  {{ o.status === 'O' ? 'Open' : 'Filed' }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </FaCard>

      <!-- What you owe + Payments -->
      <div class="grid gap-4 sm:grid-cols-2">
        <FaCard title="What you owe">
          <div v-if="cardErr.liabilities" class="text-sm text-[#c0392b]">{{ cardErr.liabilities }}</div>
          <template v-else>
            <div class="flex items-center gap-2 text-2xl font-bold">
              <i v-if="totalOutstanding === '0.00'" class="pi pi-check-circle text-[#3f8038]" />
              {{ formatMoney(totalOutstanding) }}
            </div>
            <p class="mt-0.5 text-[13px] text-fa-muted">
              {{ totalOutstanding === '0.00' ? 'All paid up — nothing due right now' : 'Outstanding with HMRC' }}
            </p>
            <div
              v-for="(l, i) in liabilities"
              :key="i"
              class="mt-3 flex items-center justify-between border-t border-[#eef1f4] pt-2.5 text-sm"
            >
              <div>
                <div>{{ l.type }}</div>
                <div v-if="l.due" class="text-[12px] text-fa-muted">due {{ formatDate(l.due) }}</div>
              </div>
              <div class="text-right">
                <div class="font-semibold">{{ formatMoney(l.outstanding_amount) }}</div>
                <div class="text-[12px] text-fa-muted">of {{ formatMoney(l.original_amount) }}</div>
              </div>
            </div>
          </template>
        </FaCard>

        <FaCard title="Payments to HMRC">
          <div v-if="cardErr.payments" class="text-sm text-[#c0392b]">{{ cardErr.payments }}</div>
          <div v-else-if="payments.length === 0" class="text-sm text-fa-muted">
            No payments in the last year.
          </div>
          <template v-else>
            <div
              v-for="(p, i) in payments"
              :key="i"
              class="flex items-center justify-between border-b border-[#eef1f4] py-2 text-sm last:border-0"
            >
              <span class="text-fa-muted">{{ p.received ? formatDate(p.received) : '—' }}</span>
              <span class="font-semibold">{{ formatMoney(p.amount) }}</span>
            </div>
            <div
              class="mt-2 flex items-center justify-between border-t border-fa-border pt-2 text-sm font-semibold"
            >
              <span>Total received</span><span>{{ formatMoney(totalPaid) }}</span>
            </div>
          </template>
        </FaCard>
      </div>

      <!-- Penalties + Registration details -->
      <div class="grid gap-4 sm:grid-cols-2">
        <FaCard title="Penalties &amp; points">
          <div v-if="cardErr.penalties" class="text-sm text-[#c0392b]">{{ cardErr.penalties }}</div>
          <template v-else-if="penalties">
            <span v-if="noPenalties" :class="[pillBase, greenPill]">
              <i class="pi pi-shield" />No penalties
            </span>
            <div class="mt-3">
              <div class="text-[13px] text-fa-muted">Late submission points</div>
              <div class="mt-1.5 flex items-center gap-2">
                <div class="flex gap-1.5">
                  <span
                    v-for="(filled, i) in pointsPips"
                    :key="i"
                    class="h-2.5 w-6 rounded-[3px] border"
                    :class="filled ? 'border-[#caa24a] bg-[#e0a93b]' : 'border-fa-border'"
                  />
                </div>
                <span class="text-[13px] text-fa-muted">
                  {{ penalties.active_points }} of {{ penalties.threshold }}
                </span>
              </div>
            </div>
            <div
              v-for="(c, i) in penalties.penalties ?? []"
              :key="i"
              class="mt-2 flex items-center justify-between border-t border-[#eef1f4] pt-2 text-sm"
            >
              <span>{{ c.type === 'late_payment' ? 'Late payment' : 'Late submission' }}</span>
              <span class="font-semibold">{{ formatMoney(c.amount) }}</span>
            </div>
          </template>
        </FaCard>

        <FaCard title="Registration details" note="From HMRC">
          <div v-if="cardErr.information" class="text-sm text-[#c0392b]">{{ cardErr.information }}</div>
          <table v-else-if="information" class="w-full text-sm">
            <tbody>
              <tr v-if="information.business_name">
                <td class="py-1.5 text-fa-muted">Name</td>
                <td class="py-1.5 text-right font-medium">{{ information.business_name }}</td>
              </tr>
              <tr v-if="settings?.vrn">
                <td class="py-1.5 text-fa-muted">VRN</td>
                <td class="py-1.5 text-right font-medium">{{ settings.vrn }}</td>
              </tr>
              <tr v-if="information.registration_date">
                <td class="py-1.5 text-fa-muted">Registered</td>
                <td class="py-1.5 text-right">{{ formatDate(information.registration_date) }}</td>
              </tr>
              <tr v-if="registrationAddress">
                <td class="py-1.5 align-top text-fa-muted">Address</td>
                <td class="py-1.5 text-right">{{ registrationAddress }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="text-sm text-fa-muted">No registration details returned.</div>
        </FaCard>
      </div>

      <p class="mt-1 text-[12px] text-fa-muted">
        <i class="pi pi-refresh mr-1" />Synced live from HMRC.
      </p>
    </template>
  </div>
</template>
