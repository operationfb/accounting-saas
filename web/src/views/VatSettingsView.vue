<script setup lang="ts">
// VAT Registration — the organisation's VAT settings screen, modelled on
// FreeAgent's "UK VAT Registration". Like Company Details it is a SINGLETON
// (GET/PUT /api/v1/vat/settings, the org comes from the token), so there is no
// create mode and no id in the URL: the page always loads the caller's org.
//
// One page, two states by role (no view/edit toggle):
//   - owner/admin → the editable form, with Save changes / Cancel.
//   - everyone else → the same layout but disabled, with no Save (read-only).
//
// "Are you VAT Registered?" is the master switch: the VAT settings / dates /
// return settings cards only appear once Registered (mirrors FreeAgent), and the
// backend only requires those fields when registered.
import { ref, reactive, computed, onMounted } from 'vue'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import RadioButton from 'primevue/radiobutton'
import DatePicker from 'primevue/datepicker'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { useAuthStore } from '@/stores/auth'
import { getVatSettings, updateVatSettings } from '@/services/vat.service'
import {
  VAT_REGISTERED_OPTIONS,
  RETURN_FREQUENCY_OPTIONS,
  ACCOUNTING_BASIS_OPTIONS,
  PRE_REG_OPTIONS,
  type VatSettings,
  type VatSettingsRequest,
} from '@/types/vat'
import { toISODate } from '@/lib/format'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()

// Only owners/admins may edit (mirrors the backend's owner/admin-only PUT). For
// everyone else the fields render disabled and the Save/Cancel actions are hidden.
const canEdit = computed(() => auth.isOrgAdmin)

// VRN as stored/sent: bare 9 digits. Be forgiving on input (strip a GB prefix +
// spaces) before checking, exactly like the backend's normaliseVRN.
function cleanVrn(v: string): string {
  return v.replace(/\s+/g, '').toUpperCase().replace(/^GB/, '')
}
const VRN_RE = /^\d{9}$/

// Parse a YYYY-MM-DD API string into a LOCAL Date (no timezone shift) for the
// DatePicker. new Date('2026-03-01') would parse as UTC midnight and can render
// as the previous day in negative-offset zones, so build it from parts.
function parseYmd(s: string | null | undefined): Date | null {
  if (!s) return null
  const [y, m, d] = s.split('-').map(Number)
  if (!y || !m || !d) return null
  return new Date(y, m - 1, d)
}

// --- form state (seeded with sensible defaults matching the screenshot) ---
const defaults = () => ({
  vatRegistered: false,
  vrn: '',
  usesNonStandardRates: false,
  effectiveDate: null as Date | null,
  firstReturnPeriodEnd: null as Date | null,
  returnFrequency: 'quarterly',
  accountingBasis: 'invoice',
  flatRateScheme: false,
  flatRatePercentage: '',
  preRegMonths: null as number | null,
})
const form = reactive(defaults())

// --- load state ---
const loading = ref(true)
const loadError = ref('')
const loaded = ref<VatSettings | null>(null) // last saved record (for Cancel)

function hydrate(s: VatSettings) {
  form.vatRegistered = s.vat_registered
  form.vrn = s.vrn ?? ''
  form.usesNonStandardRates = s.uses_non_standard_rates
  form.effectiveDate = parseYmd(s.effective_date)
  form.firstReturnPeriodEnd = parseYmd(s.first_return_period_end)
  form.returnFrequency = s.return_frequency ?? 'quarterly'
  form.accountingBasis = s.accounting_basis ?? 'invoice'
  form.flatRateScheme = s.flat_rate_scheme
  form.flatRatePercentage = s.flat_rate_percentage ?? ''
  form.preRegMonths = s.pre_reg_expense_months ?? null
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const s = await getVatSettings()
    loaded.value = s
    hydrate(s)
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load your VAT settings.'
  } finally {
    loading.value = false
  }
}

onMounted(load)

// --- validation (only enforced while Registered, mirroring the backend) ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]

  if (form.vatRegistered) {
    if (!VRN_RE.test(cleanVrn(form.vrn))) {
      errors.vrn = 'Enter your 9-digit VAT registration number.'
    }
    if (!form.effectiveDate) errors.effectiveDate = 'Enter your effective date of VAT registration.'
    if (!form.firstReturnPeriodEnd) {
      errors.firstReturnPeriodEnd = 'Enter your first VAT return period end date.'
    }
    if (!form.returnFrequency) errors.returnFrequency = 'Choose how often you file.'
    if (!form.accountingBasis) errors.accountingBasis = 'Choose your VAT accounting basis.'
    if (form.flatRateScheme && form.flatRatePercentage.trim() === '') {
      errors.flatRatePercentage = 'Enter your flat rate percentage.'
    }
  }

  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')
const successMessage = ref('')

function buildPayload(): VatSettingsRequest {
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  return {
    vat_registered: form.vatRegistered,
    vrn: form.vrn.trim() ? cleanVrn(form.vrn) : undefined,
    uses_non_standard_rates: form.usesNonStandardRates,
    effective_date: form.effectiveDate ? toISODate(form.effectiveDate) : undefined,
    first_return_period_end: form.firstReturnPeriodEnd
      ? toISODate(form.firstReturnPeriodEnd)
      : undefined,
    return_frequency: form.returnFrequency || undefined,
    accounting_basis: form.accountingBasis || undefined,
    flat_rate_scheme: form.flatRateScheme,
    // Only send a percentage when actually on the scheme.
    flat_rate_percentage: form.flatRateScheme ? opt(form.flatRatePercentage) : undefined,
    pre_reg_expense_months: form.preRegMonths,
  }
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  successMessage.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    const updated = await updateVatSettings(buildPayload())
    loaded.value = updated
    hydrate(updated)
    successMessage.value = 'VAT settings saved.'
  } catch (err) {
    formError.value = (err as ApiError)?.message ?? 'Could not save your changes. Please try again.'
  } finally {
    submitting.value = false
  }
}

function cancel() {
  if (loaded.value) hydrate(loaded.value)
  for (const k of Object.keys(errors)) delete errors[k]
  formError.value = ''
  successMessage.value = ''
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">UK VAT Registration</h1>

    <!-- Loading -->
    <FaCard v-if="loading" title="VAT registration status">
      <div class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
    </FaCard>

    <!-- Load error -->
    <FaCard v-else-if="loadError" title="VAT registration status">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Try again" severity="secondary" outlined @click="load" />
      </div>
    </FaCard>

    <!-- The form (loaded ok) -->
    <template v-else>
      <div
        v-if="formError"
        class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
        role="alert"
      >
        {{ formError }}
      </div>
      <div
        v-if="successMessage"
        class="mb-4 rounded border border-[#cfe9c7] bg-[#eaf7e6] px-3 py-2 text-sm text-[#3f8038]"
        role="status"
      >
        {{ successMessage }}
      </div>
      <div
        v-if="!canEdit"
        class="mb-4 rounded border border-fa-border bg-fa-card-header px-3 py-2 text-sm text-fa-muted"
        role="note"
      >
        Only owners and admins can edit VAT settings.
      </div>

      <!-- 1. VAT registration status -->
      <FaCard title="VAT registration status" note="Required fields *">
        <FormRow label="Are you VAT Registered?" label-for="vat-registered" required>
          <Select
            id="vat-registered"
            v-model="form.vatRegistered"
            :options="VAT_REGISTERED_OPTIONS"
            option-label="label"
            option-value="value"
            class="w-full sm:w-72"
            :disabled="!canEdit"
          />
        </FormRow>
      </FaCard>

      <!-- The rest only applies once registered (mirrors FreeAgent). -->
      <template v-if="form.vatRegistered">
        <!-- 2. VAT settings -->
        <FaCard title="VAT settings" note="Required fields *">
          <FormRow label="VAT Registration Number" label-for="vrn" required>
            <InputText
              id="vrn"
              v-model="form.vrn"
              class="w-full sm:w-56"
              placeholder="123456789"
              :invalid="!!errors.vrn"
              :disabled="!canEdit"
            />
            <p class="text-xs text-fa-muted">The 9 digit number on your VAT registration certificate.</p>
            <p v-if="errors.vrn" class="text-xs text-[#c0392b]">{{ errors.vrn }}</p>
          </FormRow>
          <FormRow label="Do you need to use VAT rates other than standard UK ones?">
            <div class="flex items-center gap-5">
              <span class="flex items-center gap-2">
                <RadioButton
                  v-model="form.usesNonStandardRates"
                  :value="false"
                  input-id="nsr-no"
                  :disabled="!canEdit"
                />
                <label for="nsr-no" class="text-sm">No</label>
              </span>
              <span class="flex items-center gap-2">
                <RadioButton
                  v-model="form.usesNonStandardRates"
                  :value="true"
                  input-id="nsr-yes"
                  :disabled="!canEdit"
                />
                <label for="nsr-yes" class="text-sm">Yes</label>
              </span>
            </div>
            <p class="text-xs text-fa-muted">
              For example, if you trade outside the UK, use VAT MOSS or the domestic reverse charge.
            </p>
          </FormRow>
        </FaCard>

        <!-- 3. Important dates on your VAT registration certificate -->
        <FaCard title="Important dates on your VAT registration certificate" note="Required fields *">
          <p class="mb-2 text-sm text-fa-muted">
            You will find these dates on the VAT registration certificate HMRC sent you. Make sure
            you copy them exactly.
          </p>
          <FormRow label="Effective Date of VAT Registration" label-for="effective-date" required>
            <DatePicker
              id="effective-date"
              v-model="form.effectiveDate"
              date-format="dd M yy"
              show-icon
              :show-on-focus="false"
              placeholder="dd mmm yy"
              :invalid="!!errors.effectiveDate"
              :disabled="!canEdit"
              class="w-full sm:w-56"
            />
            <p v-if="errors.effectiveDate" class="text-xs text-[#c0392b]">{{ errors.effectiveDate }}</p>
          </FormRow>
          <FormRow label="First VAT return period end date" label-for="first-return-end" required>
            <DatePicker
              id="first-return-end"
              v-model="form.firstReturnPeriodEnd"
              date-format="dd M yy"
              show-icon
              :show-on-focus="false"
              placeholder="dd mmm yy"
              :invalid="!!errors.firstReturnPeriodEnd"
              :disabled="!canEdit"
              class="w-full sm:w-56"
            />
            <p class="text-xs text-fa-muted">The end date of your first VAT Return can be found on your VAT certificate.</p>
            <p v-if="errors.firstReturnPeriodEnd" class="text-xs text-[#c0392b]">
              {{ errors.firstReturnPeriodEnd }}
            </p>
          </FormRow>
          <FormRow label="Frequency of returns" label-for="frequency">
            <Select
              id="frequency"
              v-model="form.returnFrequency"
              :options="RETURN_FREQUENCY_OPTIONS"
              option-label="label"
              option-value="value"
              class="w-full sm:w-56"
              :disabled="!canEdit"
            />
            <p class="text-xs text-fa-muted">Changing this setting will only affect future VAT returns.</p>
          </FormRow>
        </FaCard>

        <!-- 4. Initial VAT return settings -->
        <FaCard title="Initial VAT return settings">
          <p class="mb-2 text-sm text-fa-muted">
            We need to know a few more details to generate your VAT returns up to the present day.
          </p>
          <FormRow label="VAT Accounting Basis" label-for="basis">
            <Select
              id="basis"
              v-model="form.accountingBasis"
              :options="ACCOUNTING_BASIS_OPTIONS"
              option-label="label"
              option-value="value"
              class="w-full sm:w-56"
              :disabled="!canEdit"
            />
          </FormRow>
          <FormRow label="Are you on the Flat Rate Scheme?">
            <div class="flex items-center gap-5">
              <span class="flex items-center gap-2">
                <RadioButton
                  v-model="form.flatRateScheme"
                  :value="false"
                  input-id="frs-no"
                  :disabled="!canEdit"
                />
                <label for="frs-no" class="text-sm">No</label>
              </span>
              <span class="flex items-center gap-2">
                <RadioButton
                  v-model="form.flatRateScheme"
                  :value="true"
                  input-id="frs-yes"
                  :disabled="!canEdit"
                />
                <label for="frs-yes" class="text-sm">Yes</label>
              </span>
            </div>
          </FormRow>
          <FormRow v-if="form.flatRateScheme" label="Flat rate percentage" label-for="flat-rate" required>
            <div class="flex items-center gap-2">
              <InputText
                id="flat-rate"
                v-model="form.flatRatePercentage"
                class="w-28"
                inputmode="decimal"
                placeholder="e.g. 10.5"
                :invalid="!!errors.flatRatePercentage"
                :disabled="!canEdit"
              />
              <span class="text-sm text-fa-muted">%</span>
            </div>
            <p v-if="errors.flatRatePercentage" class="text-xs text-[#c0392b]">
              {{ errors.flatRatePercentage }}
            </p>
          </FormRow>
          <FormRow label="Include pre-registration expenses from" label-for="pre-reg">
            <Select
              id="pre-reg"
              v-model="form.preRegMonths"
              :options="PRE_REG_OPTIONS"
              option-label="label"
              option-value="value"
              class="w-full sm:w-80"
              :disabled="!canEdit"
            />
            <p class="text-xs text-fa-muted">
              Pre-registration expenses will be included on your first ever VAT return after your
              registration date.
            </p>
          </FormRow>
        </FaCard>
      </template>

      <div v-if="canEdit" class="flex items-center gap-3 py-2 pb-6">
        <Button label="Save changes" :loading="submitting" @click="submit" />
        <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
