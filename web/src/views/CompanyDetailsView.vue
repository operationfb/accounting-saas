<script setup lang="ts">
// Company Details — the organisation's own settings screen, modelled on
// FreeAgent's "Company Details". It is a SINGLETON (GET/PUT /api/v1/organisation,
// the org comes from the token), so unlike the Contact form there is no create
// mode and no id in the URL: the page always loads the caller's org.
//
// One page, two states by role (no view/edit toggle):
//   - owner/admin → the editable form, with Save changes / Cancel.
//   - everyone else → the same layout but disabled, with no Save (read-only).
// The three cards mirror the screenshot: Company details, Other details,
// About your business.
import { ref, reactive, computed, onMounted } from 'vue'
import { useRoute, RouterLink } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Textarea from 'primevue/textarea'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { useAuthStore } from '@/stores/auth'
import { getOrganisation, updateOrganisation } from '@/services/organisation.service'
import { getAdminOrganisationDetails, updateAdminOrganisationDetails } from '@/services/admin.service'
import { listCurrencies } from '@/services/currencies.service'
import { COMPANY_TYPE_OPTIONS, type OrganisationDetails } from '@/types/organisation'
import type { UpdateOrganisationRequest } from '@/types/organisation'
import { COUNTRIES } from '@/lib/countries'
import { buildCurrencyOptions, type CurrencyOption } from '@/lib/currency'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()
const route = useRoute()

// Dual-mode (mirrors MyDetailsView's self vs admin modes):
//   - SELF  (/company-details)               → the caller's own org, edit if owner/admin.
//   - ADMIN (/admin/organisations/:id/…)     → a chosen org (god view), edit if superuser.
// The route param drives the data source, the auth check, and where "back" goes.
const adminOrgId = computed(() => (route.params.id as string | undefined) || '')
const isAdminMode = computed(() => adminOrgId.value !== '')

// Who may edit: owner/admin of their own org (self), or a platform superuser (admin
// mode). For everyone else the fields render disabled and Save/Cancel are hidden.
const canEdit = computed(() =>
  isAdminMode.value ? !!auth.user?.is_superuser : auth.isOrgAdmin,
)

// Simple email shape check (the backend's `email` binding is the final authority).
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

const countryOptions = COUNTRIES.map((c) => ({ label: c.name, value: c.code }))

// Currency options for the (read-only) Native currency Select — loaded from the
// currencies API so the code renders with a friendly "GBP - British Pound" label,
// exactly like the editable pickers on Bills/Projects. The field itself is always
// disabled: native_currency is fixed at organisation creation and immutable here.
const currencyOptions = ref<CurrencyOption[]>([])

// --- form state (seeded with the backend's defaults) ---
// legalName has no visible field: the backend treats legal_name as a form-owned
// column, so a PUT that omitted it would NULL it. We load it and send it back
// unchanged to preserve it. The screenshot has no Legal name field.
const defaults = () => ({
  companyType: '',
  name: '',
  legalName: '',
  addressLine1: '',
  addressLine2: '',
  addressLine3: '',
  town: '',
  region: '',
  postcode: '',
  countryCode: 'GB',
  nativeCurrency: 'GBP',
  businessPhone: '',
  companiesHouseNumber: '',
  payeReference: '',
  accountsOfficeReference: '',
  claimsEmploymentAllowance: true,
  utr: '',
  contactEmail: '',
  contactPhone: '',
  website: '',
  businessCategory: '',
  businessDescription: '',
})
const form = reactive(defaults())

// --- load state ---
const loading = ref(true) // spinner until the org is fetched
const loadError = ref('')
const loaded = ref<OrganisationDetails | null>(null) // last saved record (for Cancel)

// Copy a fetched/updated record into the reactive form.
function hydrate(o: OrganisationDetails) {
  form.companyType = o.company_type ?? ''
  form.name = o.name ?? ''
  form.legalName = o.legal_name ?? ''
  form.addressLine1 = o.address_line_1 ?? ''
  form.addressLine2 = o.address_line_2 ?? ''
  form.addressLine3 = o.address_line_3 ?? ''
  form.town = o.town ?? ''
  form.region = o.region ?? ''
  form.postcode = o.postcode ?? ''
  form.countryCode = o.country_code || 'GB'
  form.nativeCurrency = o.native_currency || 'GBP'
  form.businessPhone = o.business_phone ?? ''
  form.companiesHouseNumber = o.companies_house_number ?? ''
  form.payeReference = o.paye_reference ?? ''
  form.accountsOfficeReference = o.accounts_office_reference ?? ''
  form.claimsEmploymentAllowance = o.claims_employment_allowance
  form.utr = o.utr ?? ''
  form.contactEmail = o.contact_email ?? ''
  form.contactPhone = o.contact_phone ?? ''
  form.website = o.website ?? ''
  form.businessCategory = o.business_category ?? ''
  form.businessDescription = o.business_description ?? ''
}

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    // Fetch the org and the currency catalogue together; the catalogue only drives
    // the read-only Native currency label, so a failure there shouldn't block the
    // page — fall back to an empty list (the Select then shows the bare code).
    const [o, currencies] = await Promise.all([
      isAdminMode.value ? getAdminOrganisationDetails(adminOrgId.value) : getOrganisation(),
      listCurrencies().catch(() => []),
    ])
    currencyOptions.value = buildCurrencyOptions(currencies)
    loaded.value = o
    hydrate(o)
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load your company details.'
  } finally {
    loading.value = false
  }
}

onMounted(load)

// --- validation ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]

  // Company name is the org's NOT NULL primary name (the backend requires it).
  if (form.name.trim() === '') errors.name = 'Enter your company name.'

  // The screenshot stars these; they apply to any company type.
  if (form.addressLine1.trim() === '') errors.addressLine1 = 'Enter your company address.'
  if (form.town.trim() === '') errors.town = 'Enter your town or city.'
  if (form.postcode.trim() === '') errors.postcode = 'Enter your post/zip code.'

  // A registration number only exists for incorporated types — required for
  // limited companies / corporations, but not sole traders or landlords.
  if (
    (form.companyType === 'limited' || form.companyType === 'corporation') &&
    form.companiesHouseNumber.trim() === ''
  ) {
    errors.companiesHouseNumber = 'Enter your company registration number.'
  }

  if (form.contactEmail.trim() && !EMAIL_RE.test(form.contactEmail.trim())) {
    errors.contactEmail = 'Enter a valid email address.'
  }

  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')
const successMessage = ref('')

function buildPayload(): UpdateOrganisationRequest {
  // Trim, and omit empty optionals so the backend stores NULL rather than "".
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  return {
    name: form.name.trim(),
    legal_name: opt(form.legalName), // round-tripped (no visible field) to preserve it
    company_type: form.companyType ? form.companyType : undefined,
    companies_house_number: opt(form.companiesHouseNumber),
    utr: opt(form.utr), // "Corporation Tax Reference"
    paye_reference: opt(form.payeReference),
    accounts_office_reference: opt(form.accountsOfficeReference),
    claims_employment_allowance: form.claimsEmploymentAllowance,
    address_line_1: opt(form.addressLine1),
    address_line_2: opt(form.addressLine2),
    address_line_3: opt(form.addressLine3),
    town: opt(form.town),
    region: opt(form.region),
    postcode: opt(form.postcode),
    // country_code + native_currency are NOT sent: both are fixed at creation and
    // immutable. The backend preserves the stored values regardless.
    business_phone: opt(form.businessPhone),
    contact_email: opt(form.contactEmail),
    contact_phone: opt(form.contactPhone),
    website: opt(form.website),
    business_category: opt(form.businessCategory),
    business_description: opt(form.businessDescription),
  }
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  successMessage.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    const updated = isAdminMode.value
      ? await updateAdminOrganisationDetails(adminOrgId.value, buildPayload())
      : await updateOrganisation(buildPayload())
    loaded.value = updated
    hydrate(updated)
    // Only in SELF mode does this org back the top bar — keep its name/country in
    // sync after a rename. In admin mode we're editing a DIFFERENT org, so don't.
    if (!isAdminMode.value) {
      auth.patchOrganisationSummary({ name: updated.name, country_code: updated.country_code })
    }
    successMessage.value = 'Company details saved.'
  } catch (err) {
    // 401 is handled by apiFetch. 400/403/422 land here.
    formError.value =
      (err as ApiError)?.message ?? 'Could not save your changes. Please try again.'
  } finally {
    submitting.value = false
  }
}

// Cancel discards edits by re-applying the last saved record (this is a settings
// singleton, so there's no list to navigate back to).
function cancel() {
  if (loaded.value) hydrate(loaded.value)
  for (const k of Object.keys(errors)) delete errors[k]
  formError.value = ''
  successMessage.value = ''
}
</script>

<template>
  <AppLayout>
    <!-- Admin mode: a back link to the all-organisations list, so the god-view
         context is clear (we're editing a specific org, not the caller's own). -->
    <RouterLink
      v-if="isAdminMode"
      to="/admin/organisations"
      class="mb-2 inline-block text-sm text-fa-blue hover:underline"
    >
      ← All organisations
    </RouterLink>
    <h1 class="mb-[18px] text-[22px] font-bold">
      Company Details
      <span v-if="isAdminMode && loaded" class="font-normal text-fa-muted">— {{ loaded.name }}</span>
    </h1>

    <!-- Loading -->
    <FaCard v-if="loading" title="Company details">
      <div class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
    </FaCard>

    <!-- Load error -->
    <FaCard v-else-if="loadError" title="Company details">
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
        Only owners and admins can edit company details.
      </div>

      <!-- 1. Company details -->
      <FaCard title="Company details" note="Required fields *">
        <FormRow label="Company type" label-for="company-type">
          <Select
            id="company-type"
            v-model="form.companyType"
            :options="COMPANY_TYPE_OPTIONS"
            option-label="label"
            option-value="value"
            placeholder="Select a company type"
            show-clear
            class="w-full sm:w-72"
            :disabled="!canEdit"
          />
        </FormRow>
        <FormRow label="Company name" label-for="company-name" required>
          <InputText
            id="company-name"
            v-model="form.name"
            class="w-full max-w-md"
            :invalid="!!errors.name"
            :disabled="!canEdit"
          />
          <p v-if="errors.name" class="text-xs text-[#c0392b]">{{ errors.name }}</p>
        </FormRow>
        <FormRow label="Company address" label-for="address-1" required>
          <InputText
            id="address-1"
            v-model="form.addressLine1"
            class="w-full max-w-md"
            :invalid="!!errors.addressLine1"
            :disabled="!canEdit"
          />
          <InputText
            v-model="form.addressLine2"
            class="w-full max-w-md"
            aria-label="Address line 2"
            :disabled="!canEdit"
          />
          <InputText
            v-model="form.addressLine3"
            class="w-full max-w-md"
            aria-label="Address line 3"
            :disabled="!canEdit"
          />
          <p v-if="errors.addressLine1" class="text-xs text-[#c0392b]">{{ errors.addressLine1 }}</p>
        </FormRow>
        <FormRow label="Town" label-for="town" required>
          <InputText
            id="town"
            v-model="form.town"
            class="w-full sm:w-72"
            :invalid="!!errors.town"
            :disabled="!canEdit"
          />
          <p v-if="errors.town" class="text-xs text-[#c0392b]">{{ errors.town }}</p>
        </FormRow>
        <FormRow label="Region or State" label-for="region">
          <InputText id="region" v-model="form.region" class="w-full sm:w-72" :disabled="!canEdit" />
        </FormRow>
        <FormRow label="Post/Zip code" label-for="postcode" required>
          <InputText
            id="postcode"
            v-model="form.postcode"
            class="w-full sm:w-40"
            :invalid="!!errors.postcode"
            :disabled="!canEdit"
          />
          <p v-if="errors.postcode" class="text-xs text-[#c0392b]">{{ errors.postcode }}</p>
        </FormRow>
        <!-- Country + Native currency are fixed at organisation creation and
             immutable here — always disabled, regardless of role. -->
        <FormRow label="Country" label-for="country">
          <Select
            id="country"
            v-model="form.countryCode"
            :options="countryOptions"
            option-label="label"
            option-value="value"
            filter
            filter-placeholder="Search countries"
            class="w-full sm:w-72"
            disabled
          />
          <p class="text-xs text-fa-muted">Set when the organisation was created and can't be changed.</p>
        </FormRow>
        <FormRow label="Native currency" label-for="native-currency">
          <Select
            id="native-currency"
            v-model="form.nativeCurrency"
            :options="currencyOptions"
            option-label="label"
            option-value="value"
            class="w-full sm:w-72"
            disabled
          />
          <p class="text-xs text-fa-muted">Set when the organisation was created and can't be changed.</p>
        </FormRow>
        <FormRow label="Business phone number" label-for="business-phone">
          <InputText
            id="business-phone"
            v-model="form.businessPhone"
            class="w-full sm:w-56"
            :disabled="!canEdit"
          />
        </FormRow>
        <FormRow label="Company Registration Number" label-for="crn">
          <InputText
            id="crn"
            v-model="form.companiesHouseNumber"
            class="w-full sm:w-56"
            :invalid="!!errors.companiesHouseNumber"
            :disabled="!canEdit"
          />
          <p v-if="errors.companiesHouseNumber" class="text-xs text-[#c0392b]">
            {{ errors.companiesHouseNumber }}
          </p>
        </FormRow>
        <FormRow label="PAYE Reference" label-for="paye">
          <InputText id="paye" v-model="form.payeReference" class="w-full sm:w-56" :disabled="!canEdit" />
          <p class="text-xs text-fa-muted">e.g. 123/A246</p>
        </FormRow>
        <FormRow label="Employment allowance">
          <div class="flex flex-col gap-2">
            <div class="flex items-center gap-6">
              <label class="flex items-center gap-2">
                <input
                  type="radio"
                  name="ea"
                  :value="false"
                  :checked="!form.claimsEmploymentAllowance"
                  :disabled="!canEdit"
                  @change="form.claimsEmploymentAllowance = false"
                />
                <span>Don't claim</span>
              </label>
              <label class="flex items-center gap-2">
                <input
                  type="radio"
                  name="ea"
                  :value="true"
                  :checked="form.claimsEmploymentAllowance"
                  :disabled="!canEdit"
                  @change="form.claimsEmploymentAllowance = true"
                />
                <span>Claim</span>
              </label>
            </div>
            <p class="max-w-xl text-xs text-fa-muted">
              Each payroll year, make sure you double check whether you're eligible before claiming
              Employment Allowance. When claimed, the payroll run offsets the company's employer NI
              by up to the annual cap.
            </p>
          </div>
        </FormRow>
        <FormRow label="Accounts Office Reference" label-for="aor">
          <InputText
            id="aor"
            v-model="form.accountsOfficeReference"
            class="w-full sm:w-56"
            :disabled="!canEdit"
          />
          <p class="text-xs text-fa-muted">e.g. 123PA00045678</p>
        </FormRow>
        <FormRow label="Corporation Tax Reference" label-for="utr">
          <InputText id="utr" v-model="form.utr" class="w-full sm:w-56" :disabled="!canEdit" />
          <p class="text-xs text-fa-muted">Also known as a COTAX Reference e.g. 1234567890</p>
        </FormRow>
      </FaCard>

      <!-- 2. Other details -->
      <FaCard title="Other details">
        <p class="mb-2 text-sm text-fa-muted">
          These details will be included on invoices or estimates for your contacts.
        </p>
        <FormRow label="Contact email address" label-for="contact-email">
          <InputText
            id="contact-email"
            v-model="form.contactEmail"
            class="w-full max-w-md"
            :invalid="!!errors.contactEmail"
            :disabled="!canEdit"
          />
          <p v-if="errors.contactEmail" class="text-xs text-[#c0392b]">{{ errors.contactEmail }}</p>
        </FormRow>
        <FormRow label="Contact phone number" label-for="contact-phone">
          <InputText
            id="contact-phone"
            v-model="form.contactPhone"
            class="w-full sm:w-56"
            :disabled="!canEdit"
          />
        </FormRow>
        <FormRow label="Website" label-for="website">
          <InputText
            id="website"
            v-model="form.website"
            class="w-full max-w-md"
            :disabled="!canEdit"
          />
        </FormRow>
      </FaCard>

      <!-- 3. About your business -->
      <FaCard title="About your business">
        <FormRow label="Business category" label-for="business-category">
          <InputText
            id="business-category"
            v-model="form.businessCategory"
            class="w-full max-w-md"
            :disabled="!canEdit"
          />
        </FormRow>
        <FormRow label="Business description" label-for="business-description">
          <Textarea
            id="business-description"
            v-model="form.businessDescription"
            rows="3"
            class="w-full max-w-md"
            :disabled="!canEdit"
          />
          <p class="text-xs text-fa-muted">A brief description of your business</p>
        </FormRow>
      </FaCard>

      <div v-if="canEdit" class="flex items-center gap-3 py-2 pb-6">
        <Button label="Save changes" :loading="submitting" @click="submit" />
        <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
