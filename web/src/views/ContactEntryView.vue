<script setup lang="ts">
// Contact form — DUAL-MODE:
//   /contacts/new       → create  (POST /api/v1/contacts)
//   /contacts/:id/edit  → edit    (PUT  /api/v1/contacts/:id), pre-filled from the record
// Modelled on ExpenseEntryView, but simpler: contacts have no OCR, VAT,
// attachments, or money. The four cards mirror the FreeAgent "New Contact" screen.
import { ref, reactive, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Checkbox from 'primevue/checkbox'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { getContact, createContact, updateContact } from '@/services/contacts.service'
import { COUNTRIES } from '@/lib/countries'
import type { CreateContactRequest } from '@/types/contact'
import type { ApiError } from '@/lib/api'

const router = useRouter()
const route = useRoute()

// Edit mode iff we're on /contacts/:id/edit (the create route has no :id param).
const editId = typeof route.params.id === 'string' ? route.params.id : undefined
const isEdit = !!editId

// Simple email shape check (the backend's `email` binding is the final authority).
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// --- option lists ---
const countryOptions = COUNTRIES.map((c) => ({ label: c.name, value: c.code }))
const chargeVatOptions = [
  { label: 'Always', value: 'ALWAYS' },
  { label: 'Never', value: 'NEVER' },
  { label: 'Only if contact is in the same country', value: 'SAME_COUNTRY' },
]
const languageOptions = [
  { label: 'English', value: 'en' },
  { label: 'French', value: 'fr' },
  { label: 'German', value: 'de' },
  { label: 'Spanish', value: 'es' },
  { label: 'Italian', value: 'it' },
]

// --- form state (seeded with the backend's defaults) ---
const defaults = () => ({
  firstName: '',
  lastName: '',
  organisationName: '',
  email: '',
  billingEmail: '',
  telephone: '',
  mobile: '',
  addressLine1: '',
  addressLine2: '',
  addressLine3: '',
  town: '',
  region: '',
  postcode: '',
  countryCode: 'GB',
  paymentTermsDays: '',
  usesContactEmail: false,
  usesContactInvoiceSeq: false,
  displayContactName: true,
  chargeVat: 'SAME_COUNTRY',
  vatRegistrationNumber: '',
  invoiceLanguage: 'en',
  bankSortCode: '',
  bankAccountNumber: '',
  bankRecipientName: '',
})
const form = reactive(defaults())

// --- edit-mode load state ---
const loadingContact = ref(isEdit) // spinner until the record is ready
const loadError = ref('')

async function loadForEdit() {
  if (!editId) return
  loadingContact.value = true
  loadError.value = ''
  try {
    const c = await getContact(editId)
    form.firstName = c.first_name ?? ''
    form.lastName = c.last_name ?? ''
    form.organisationName = c.organisation_name ?? ''
    form.email = c.email ?? ''
    form.billingEmail = c.billing_email ?? ''
    form.telephone = c.telephone ?? ''
    form.mobile = c.mobile ?? ''
    form.addressLine1 = c.address_line_1 ?? ''
    form.addressLine2 = c.address_line_2 ?? ''
    form.addressLine3 = c.address_line_3 ?? ''
    form.town = c.town ?? ''
    form.region = c.region ?? ''
    form.postcode = c.postcode ?? ''
    form.countryCode = c.country_code || 'GB'
    // 0 is meaningful ("Due on Receipt"), so keep it; null/undefined → blank.
    form.paymentTermsDays = c.default_payment_terms_days != null ? String(c.default_payment_terms_days) : ''
    form.usesContactEmail = c.uses_contact_level_email_settings
    form.usesContactInvoiceSeq = c.uses_contact_level_invoice_sequence
    form.displayContactName = c.display_contact_name
    form.chargeVat = c.charge_vat || 'SAME_COUNTRY'
    form.vatRegistrationNumber = c.vat_registration_number ?? ''
    form.invoiceLanguage = c.invoice_language || 'en'
    form.bankSortCode = c.bank_sort_code ?? ''
    form.bankAccountNumber = c.bank_account_number ?? ''
    form.bankRecipientName = c.bank_recipient_name ?? ''
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load this contact.'
  } finally {
    loadingContact.value = false
  }
}

onMounted(() => {
  if (isEdit) loadForEdit()
})

// --- validation ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]

  // Name-or-org rule (the app-layer rule deferred from the DB): a contact needs an
  // organisation name OR a first + last name.
  const hasOrg = form.organisationName.trim() !== ''
  const hasPerson = form.firstName.trim() !== '' && form.lastName.trim() !== ''
  if (!hasOrg && !hasPerson) {
    errors.name = 'Enter a first and last name, and/or an organisation name.'
  }

  if (form.email.trim() && !EMAIL_RE.test(form.email.trim())) {
    errors.email = 'Enter a valid email address.'
  }
  if (form.billingEmail.trim() && !EMAIL_RE.test(form.billingEmail.trim())) {
    errors.billingEmail = 'Enter a valid email address.'
  }

  const terms = form.paymentTermsDays.trim()
  if (terms && !/^\d+$/.test(terms)) {
    errors.paymentTermsDays = 'Enter a whole number of days (0 or more).'
  }

  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')
const successMessage = ref('')

function buildPayload(): CreateContactRequest {
  const opt = (v: string) => {
    const t = v.trim()
    return t === '' ? undefined : t
  }
  const payload: CreateContactRequest = {
    first_name: opt(form.firstName),
    last_name: opt(form.lastName),
    organisation_name: opt(form.organisationName),
    email: opt(form.email),
    billing_email: opt(form.billingEmail),
    telephone: opt(form.telephone),
    mobile: opt(form.mobile),
    address_line_1: opt(form.addressLine1),
    address_line_2: opt(form.addressLine2),
    address_line_3: opt(form.addressLine3),
    town: opt(form.town),
    region: opt(form.region),
    postcode: opt(form.postcode),
    country_code: form.countryCode,
    uses_contact_level_email_settings: form.usesContactEmail,
    uses_contact_level_invoice_sequence: form.usesContactInvoiceSeq,
    display_contact_name: form.displayContactName,
    charge_vat: form.chargeVat,
    vat_registration_number: opt(form.vatRegistrationNumber),
    invoice_language: form.invoiceLanguage,
    bank_sort_code: opt(form.bankSortCode),
    bank_account_number: opt(form.bankAccountNumber),
    bank_recipient_name: opt(form.bankRecipientName),
  }
  // Days: send only when provided; 0 is preserved ("Due on Receipt").
  const terms = form.paymentTermsDays.trim()
  if (terms !== '') payload.default_payment_terms_days = Number(terms)
  return payload
}

function resetForm() {
  Object.assign(form, defaults())
  for (const k of Object.keys(errors)) delete errors[k]
}

async function submit(addAnother: boolean) {
  if (submitting.value) return
  formError.value = ''
  successMessage.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    if (editId) {
      await updateContact(editId, buildPayload())
      router.push('/contacts')
    } else {
      await createContact(buildPayload())
      if (addAnother) {
        resetForm()
        successMessage.value = 'Contact created. Add another below.'
      } else {
        router.push('/contacts')
      }
    }
  } catch (err) {
    // 401 is already handled by apiFetch. 400/403/404/422 land here.
    formError.value =
      (err as ApiError)?.message ??
      (editId ? 'Could not save your changes. Please try again.' : 'Could not create the contact. Please try again.')
  } finally {
    submitting.value = false
  }
}

function cancel() {
  router.push('/contacts')
}
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">{{ isEdit ? 'Edit Contact' : 'New Contact' }}</h1>

    <!-- Edit: loading -->
    <FaCard v-if="isEdit && loadingContact" title="Contact Details">
      <div class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
    </FaCard>

    <!-- Edit: load error -->
    <FaCard v-else-if="isEdit && loadError" title="Contact Details">
      <div class="py-8 text-center">
        <p class="mb-4 text-sm text-[#c0392b]">{{ loadError }}</p>
        <Button label="Back to contacts" severity="secondary" outlined @click="router.push('/contacts')" />
      </div>
    </FaCard>

    <!-- The form (create, or edit once the record loaded ok) -->
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

      <!-- 1. Contact Details -->
      <FaCard title="Contact Details" note="Required fields *">
        <FormRow label="First Name" label-for="first-name" required>
          <InputText id="first-name" v-model="form.firstName" class="w-72" :invalid="!!errors.name" />
        </FormRow>
        <FormRow label="Last Name" label-for="last-name" required>
          <InputText id="last-name" v-model="form.lastName" class="w-72" :invalid="!!errors.name" />
        </FormRow>
        <FormRow label="Organisation" label-for="organisation" required>
          <InputText id="organisation" v-model="form.organisationName" class="w-full max-w-md" :invalid="!!errors.name" />
          <p class="text-xs text-fa-muted">
            Enter a first and last name, and/or an organisation name. Both are not required.
          </p>
          <p v-if="errors.name" class="text-xs text-[#c0392b]">{{ errors.name }}</p>
        </FormRow>
        <FormRow label="Email" label-for="email">
          <InputText id="email" v-model="form.email" class="w-full max-w-md" :invalid="!!errors.email" />
          <p v-if="errors.email" class="text-xs text-[#c0392b]">{{ errors.email }}</p>
        </FormRow>
        <FormRow label="Billing Email" label-for="billing-email">
          <InputText id="billing-email" v-model="form.billingEmail" class="w-full max-w-md" :invalid="!!errors.billingEmail" />
          <p v-if="errors.billingEmail" class="text-xs text-[#c0392b]">{{ errors.billingEmail }}</p>
        </FormRow>
        <FormRow label="Telephone" label-for="telephone">
          <InputText id="telephone" v-model="form.telephone" class="w-56" />
        </FormRow>
        <FormRow label="Mobile Number" label-for="mobile">
          <InputText id="mobile" v-model="form.mobile" class="w-56" />
        </FormRow>
      </FaCard>

      <!-- 2. Invoicing Address -->
      <FaCard title="Invoicing Address">
        <FormRow label="Address" label-for="address-1">
          <InputText id="address-1" v-model="form.addressLine1" class="w-full max-w-md" />
          <InputText v-model="form.addressLine2" class="w-full max-w-md" aria-label="Address line 2" />
          <InputText v-model="form.addressLine3" class="w-full max-w-md" aria-label="Address line 3" />
        </FormRow>
        <FormRow label="Town" label-for="town">
          <InputText id="town" v-model="form.town" class="w-72" />
        </FormRow>
        <FormRow label="Region or State" label-for="region">
          <InputText id="region" v-model="form.region" class="w-72" />
        </FormRow>
        <FormRow label="Post/Zip code" label-for="postcode">
          <InputText id="postcode" v-model="form.postcode" class="w-40" />
        </FormRow>
        <FormRow label="Country" label-for="country">
          <Select
            id="country"
            v-model="form.countryCode"
            :options="countryOptions"
            option-label="label"
            option-value="value"
            filter
            filter-placeholder="Search countries"
            class="w-72"
          />
        </FormRow>
      </FaCard>

      <!-- 3. Invoicing Options -->
      <FaCard title="Invoicing Options">
        <FormRow label="Default Payment Terms" label-for="terms">
          <InputGroup class="w-40">
            <InputText
              id="terms"
              v-model="form.paymentTermsDays"
              inputmode="numeric"
              :invalid="!!errors.paymentTermsDays"
            />
            <InputGroupAddon>days</InputGroupAddon>
          </InputGroup>
          <p class="text-xs text-fa-muted">
            Set to zero to display ‘Due on Receipt’. Leave blank for no contact-level terms.
          </p>
          <p v-if="errors.paymentTermsDays" class="text-xs text-[#c0392b]">{{ errors.paymentTermsDays }}</p>
        </FormRow>

        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.usesContactEmail" binary input-id="uses-email" />
            <span>Use contact-level email settings?</span>
          </label>
        </FormRow>
        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.usesContactInvoiceSeq" binary input-id="uses-seq" />
            <span>Contact-level Invoice Sequence?</span>
          </label>
        </FormRow>
        <FormRow>
          <label class="inline-flex items-center gap-2 text-sm text-fa-text">
            <Checkbox v-model="form.displayContactName" binary input-id="display-name" />
            <span>Display Contact Name</span>
          </label>
        </FormRow>

        <FormRow label="Charge VAT" label-for="charge-vat">
          <Select
            id="charge-vat"
            v-model="form.chargeVat"
            :options="chargeVatOptions"
            option-label="label"
            option-value="value"
            class="w-full max-w-md"
          />
        </FormRow>
        <FormRow label="VAT Registration Number" label-for="vat-reg">
          <InputText id="vat-reg" v-model="form.vatRegistrationNumber" class="w-56" />
        </FormRow>
        <FormRow label="Invoice/Estimate Language" label-for="lang">
          <Select
            id="lang"
            v-model="form.invoiceLanguage"
            :options="languageOptions"
            option-label="label"
            option-value="value"
            class="w-56"
          />
        </FormRow>
      </FaCard>

      <!-- 4. Contact Bank Account Details -->
      <FaCard title="Contact Bank Account Details">
        <FormRow label="Sort Code" label-for="sort-code">
          <InputText id="sort-code" v-model="form.bankSortCode" class="w-40" />
        </FormRow>
        <FormRow label="Account Number" label-for="account-number">
          <InputText id="account-number" v-model="form.bankAccountNumber" class="w-56" />
        </FormRow>
        <FormRow label="Recipient name" label-for="recipient">
          <InputText id="recipient" v-model="form.bankRecipientName" class="w-72" />
        </FormRow>
      </FaCard>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button v-if="isEdit" label="Save changes" :loading="submitting" @click="submit(false)" />
        <template v-else>
          <Button label="Create new contact" :loading="submitting" @click="submit(false)" />
          <Button
            label="Create and add another"
            severity="secondary"
            outlined
            :disabled="submitting"
            @click="submit(true)"
          />
        </template>
        <button type="button" class="font-semibold text-fa-blue hover:underline" @click="cancel">Cancel</button>
      </div>
    </template>
  </AppLayout>
</template>
