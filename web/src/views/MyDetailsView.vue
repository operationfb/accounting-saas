<script setup lang="ts">
// User Details — a UNIFIED screen (repurposed from the old "My Details"). It
// serves two modes off one component, modelled on FreeAgent's User Details:
//
//   • Self mode  (route /my-details, or /users/:id where :id is you): every user
//     edits their OWN profile + payroll fields via GET/PUT /api/v1/profile. No
//     role gate (the backend is self-scoped from the token). The Email receipts
//     card (the per-user Mailgun forwarding address) shows here.
//   • Admin mode (route /users/:id for someone else): an owner/admin edits that
//     user's details, payroll fields, access role and status via
//     GET/PUT /api/v1/members/:id. A non-admin who lands here is redirected to
//     their own details.
//
// The payroll fields (National Insurance number, personal UTR, date of birth) are
// captured for the future payroll module; format is validated server-side.
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Checkbox from 'primevue/checkbox'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { useAuthStore } from '@/stores/auth'
import { getProfile, updateProfile, getInboxAddress } from '@/services/user.service'
import { getMember, updateMember, getMemberInboxAddress } from '@/services/members.service'
import type { Payroll } from '@/types/member'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

// --- mode ---
// The optional :id path param. `isSelf` drives labels, the email-receipts card and
// the top-bar name sync. `isAdminMode` drives which endpoint we use AND whether the
// payroll sections load/render: an owner/admin on a /users/:id page uses the members
// endpoint — INCLUDING viewing their own id — so they see payroll for everyone, while a
// non-admin (only ever on /my-details) uses /profile and never sees payroll. Both flags
// can be true at once (an admin viewing their own /users/:id record).
const targetId = computed(() => (route.params.id as string | undefined) || undefined)
const isSelf = computed(() => !targetId.value || targetId.value === auth.user?.id)
const isAdminMode = computed(() => !!targetId.value && auth.isOrgAdmin)

// Role + status dropdown options (admin mode). Labels are the access-control
// roles; the payroll "position" (Director/Employee) is a separate, deferred field.
// NOTE the deliberate label≠value divergence: the `member` access role is shown as
// "Employee"; the stored/API value stays `member` (don't "fix" this to match).
const roleOptions = [
  { label: 'Owner', value: 'owner' },
  { label: 'Admin', value: 'admin' },
  { label: 'Employee', value: 'member' },
  { label: 'Accountant', value: 'accountant' },
  { label: 'Read only', value: 'read_only' },
]
const statusOptions = [
  { label: 'Active', value: 'active' },
  { label: 'Suspended', value: 'suspended' },
  { label: 'Deactivated', value: 'deactivated' },
]

// ---------------------------------------------------------------------------
// Payroll (admin-only). A flat form object mirroring the API Payroll shape:
// money fields are pound strings, optional text/date fields hold '' (sent as null).
// ---------------------------------------------------------------------------
function emptyPayroll() {
  return {
    is_existing_employee: false,
    start_date: '',
    starting_declaration: '',
    nic_calculation: 'employee',
    normal_working_hours: '',
    paid_hourly: false,
    paid_irregularly: false,
    payroll_id: '',
    tax_code: '',
    week1_month1_basis: false,
    ni_category_letter: 'A',
    student_loan_undergraduate: false,
    student_loan_postgraduate: false,
    basic_pay: '0.00',
    allowance: '0.00',
    other_payments: '0.00',
    pay_not_subject_to_tax_ni: '0.00',
    receiving_statutory_pay: false,
    payroll_giving: '0.00',
    other_deductions_net_pay: '0.00',
    items_class1_nic_not_paye: '0.00',
    salary_sacrifice_deductions: '0.00',
    pension_status: 'opted_out_or_ineligible',
    leaving_next_pay_run: false,
    leaving_date: '',
  }
}
type PayrollForm = ReturnType<typeof emptyPayroll>
const payroll = reactive<PayrollForm>(emptyPayroll())
const payrollSnapshot = ref<PayrollForm | null>(null)

// Money field keys (for the v-for money rows) and a typed accessor cast.
type MoneyKey =
  | 'basic_pay' | 'allowance' | 'other_payments' | 'pay_not_subject_to_tax_ni'
  | 'payroll_giving' | 'other_deductions_net_pay' | 'items_class1_nic_not_paye'
  | 'salary_sacrifice_deductions'
const monthlyPayFields: { key: MoneyKey; label: string }[] = [
  { key: 'basic_pay', label: 'Basic Pay' },
  { key: 'allowance', label: 'Allowance' },
  { key: 'other_payments', label: 'Other Payments' },
  { key: 'pay_not_subject_to_tax_ni', label: 'Pay Not Subject to Tax or NI' },
]
const monthlyDeductionFields: { key: MoneyKey; label: string }[] = [
  { key: 'payroll_giving', label: 'Payroll Giving' },
  { key: 'other_deductions_net_pay', label: 'Other Deductions From Net Pay' },
  { key: 'items_class1_nic_not_paye', label: 'Items subject to Class 1 NIC but not taxed under PAYE' },
  { key: 'salary_sacrifice_deductions', label: 'Salary Sacrifice deductions' },
]

// Enum/boolean option lists.
const yesNo = [
  { label: 'Yes', value: true },
  { label: 'No', value: false },
]
const onPayrollOptions = [
  { label: 'Yes — existing employee for the business', value: true },
  { label: 'No — new employee for this business', value: false },
]
const startingDeclarationOptions = [
  { label: 'A — first job since 6 April, no other taxable income', value: 'A' },
  { label: "B — only job now, but had another job/benefit since 6 April", value: 'B' },
  { label: 'C — has another job or a pension', value: 'C' },
]
const nicCalculationOptions = [
  { label: 'Director', value: 'director' },
  { label: 'Director (alternative arrangements)', value: 'director_alternative' },
  { label: 'Employee', value: 'employee' },
]
const workingHoursOptions = [
  { label: 'Less than 16 hours', value: 'under_16' },
  { label: '16 or more hours, but less than 24', value: '16_to_24' },
  { label: '24 or more hours, but less than 30', value: '24_to_30' },
  { label: '30 hours or more', value: '30_plus' },
  { label: 'Other (e.g. changeable hours)', value: 'other' },
]
const niLetterOptions = ['A', 'B', 'C', 'F', 'H', 'I', 'J', 'L', 'M', 'N', 'S', 'V', 'X', 'Z'].map(
  (l) => ({ label: l, value: l }),
)
// Statutory Pay "Yes" and Pension "making contributions" are DISABLED — their
// amount detail is deferred (see plan). The radios stay for layout/clarity.
const statutoryPayOptions = [
  { label: 'Yes', value: true, disabled: true },
  { label: 'No', value: false },
]
const pensionOptions = [
  { label: 'Not yet eligible', value: 'not_yet_eligible' },
  { label: 'No, opted out or ineligible', value: 'opted_out_or_ineligible' },
  { label: 'Yes, making contributions', value: 'making_contributions', disabled: true },
]

// --- form state ---
const form = reactive({
  firstName: '',
  lastName: '',
  nino: '',
  utr: '',
  dob: '',
  // Personal/home address (free text, all optional).
  addr1: '',
  addr2: '',
  addr3: '',
  addr4: '',
  postcode: '',
  role: 'member',
  status: 'active',
})
// Read-only login email. Seed from the cached session for an instant paint, then
// overwrite with the authoritative value from the load.
const email = ref(auth.user?.email ?? '')

// --- load state ---
const loading = ref(true)
const loadError = ref('')
// Snapshot of the last-loaded values, for Cancel.
const snapshot = ref<typeof form | null>(null)

// Copy a loaded record (self User or admin MemberDetail) into the form. Role/status
// only exist in admin mode; in self mode the role is read off the scoped org.
function hydrate(u: {
  first_name: string
  last_name: string
  email: string
  national_insurance_number?: string | null
  utr?: string | null
  date_of_birth?: string | null
  address_line_1?: string | null
  address_line_2?: string | null
  address_line_3?: string | null
  address_line_4?: string | null
  postcode?: string | null
  role?: string
  status?: string
  payroll?: Payroll | null
}) {
  form.firstName = u.first_name
  form.lastName = u.last_name
  form.nino = u.national_insurance_number ?? ''
  form.utr = u.utr ?? ''
  form.dob = u.date_of_birth ?? ''
  form.addr1 = u.address_line_1 ?? ''
  form.addr2 = u.address_line_2 ?? ''
  form.addr3 = u.address_line_3 ?? ''
  form.addr4 = u.address_line_4 ?? ''
  form.postcode = u.postcode ?? ''
  form.role = u.role ?? auth.organisation?.role ?? 'member'
  form.status = u.status ?? 'active'
  email.value = u.email
  snapshot.value = { ...form }
  hydratePayroll(u.payroll)
}

// Copy the loaded payroll (admin GET) into the flat payroll form, mapping nullable
// text/date fields to ''. When there's no payroll (self/profile load), reset to
// defaults. Snapshots for Cancel.
function hydratePayroll(p?: Payroll | null) {
  const next = emptyPayroll()
  if (p) {
    Object.assign(next, {
      is_existing_employee: p.is_existing_employee,
      start_date: p.start_date ?? '',
      starting_declaration: p.starting_declaration ?? '',
      nic_calculation: p.nic_calculation,
      normal_working_hours: p.normal_working_hours ?? '',
      paid_hourly: p.paid_hourly,
      paid_irregularly: p.paid_irregularly,
      payroll_id: p.payroll_id ?? '',
      tax_code: p.tax_code ?? '',
      week1_month1_basis: p.week1_month1_basis,
      ni_category_letter: p.ni_category_letter,
      student_loan_undergraduate: p.student_loan_undergraduate,
      student_loan_postgraduate: p.student_loan_postgraduate,
      basic_pay: p.basic_pay,
      allowance: p.allowance,
      other_payments: p.other_payments,
      pay_not_subject_to_tax_ni: p.pay_not_subject_to_tax_ni,
      receiving_statutory_pay: p.receiving_statutory_pay,
      payroll_giving: p.payroll_giving,
      other_deductions_net_pay: p.other_deductions_net_pay,
      items_class1_nic_not_paye: p.items_class1_nic_not_paye,
      salary_sacrifice_deductions: p.salary_sacrifice_deductions,
      pension_status: p.pension_status,
      leaving_next_pay_run: p.leaving_next_pay_run,
      leaving_date: p.leaving_date ?? '',
    })
  }
  Object.assign(payroll, next)
  payrollSnapshot.value = { ...payroll }
}

// Build the payroll payload for the PUT: blank optional text/date → null; the leaving
// date is only sent when "leaving" = Yes.
function payrollPayload(): Payroll {
  return {
    ...payroll,
    start_date: orNull(payroll.start_date),
    starting_declaration: orNull(payroll.starting_declaration),
    normal_working_hours: orNull(payroll.normal_working_hours),
    payroll_id: orNull(payroll.payroll_id),
    tax_code: orNull(payroll.tax_code),
    leaving_date: payroll.leaving_next_pay_run ? orNull(payroll.leaving_date) : null,
  }
}

// --- email-receipts inbox (Mailgun) ---
// Shown in BOTH modes: self uses the self-scoped /inbox-address; admin uses the
// owner/admin-gated /members/:id/inbox-address for the target user.
const inboxEnabled = ref(false)
const inboxAddress = ref('')
const copied = ref(false)

async function loadInbox() {
  try {
    const res =
      isAdminMode.value && targetId.value
        ? await getMemberInboxAddress(targetId.value)
        : await getInboxAddress()
    inboxEnabled.value = res.enabled
    inboxAddress.value = res.address
  } catch {
    // Non-fatal: if the inbox lookup fails we simply don't show the card.
    inboxEnabled.value = false
  }
}

async function copyInbox() {
  try {
    await navigator.clipboard.writeText(inboxAddress.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    // Clipboard can be blocked (insecure context); the address is selectable anyway.
  }
}

async function load() {
  // Guard: a non-admin can't view/edit another user — bounce to their own details.
  if (targetId.value && targetId.value !== auth.user?.id && !auth.isOrgAdmin) {
    router.replace('/my-details')
    return
  }
  loading.value = true
  loadError.value = ''
  try {
    if (isAdminMode.value && targetId.value) {
      hydrate(await getMember(targetId.value))
    } else {
      hydrate(await getProfile())
    }
    // The inbox address loads independently (either mode) — its absence or a
    // failure must not block the page.
    void loadInbox()
  } catch (err) {
    loadError.value = (err as ApiError)?.message ?? 'Could not load the user details.'
  } finally {
    loading.value = false
  }
}

onMounted(load)
// Re-load when navigating between users (the component is reused across /users/:id).
watch(targetId, load)

// --- validation ---
const errors = reactive<Record<string, string>>({})

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (form.firstName.trim() === '') errors.firstName = 'Enter a first name.'
  if (form.lastName.trim() === '') errors.lastName = 'Enter a last name.'
  return Object.keys(errors).length === 0
}

// --- submit ---
const submitting = ref(false)
const formError = ref('')
const successMessage = ref('')

// Blank optional field → null so the server clears the column.
function orNull(s: string): string | null {
  const t = s.trim()
  return t === '' ? null : t
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  successMessage.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    if (isAdminMode.value && targetId.value) {
      const updated = await updateMember(targetId.value, {
        first_name: form.firstName.trim(),
        last_name: form.lastName.trim(),
        national_insurance_number: orNull(form.nino),
        utr: orNull(form.utr),
        date_of_birth: orNull(form.dob),
        address_line_1: orNull(form.addr1),
        address_line_2: orNull(form.addr2),
        address_line_3: orNull(form.addr3),
        address_line_4: orNull(form.addr4),
        postcode: orNull(form.postcode),
        role: form.role as 'owner' | 'admin' | 'member' | 'accountant' | 'read_only',
        status: form.status as 'active' | 'suspended' | 'deactivated',
        payroll: payrollPayload(),
      })
      hydrate(updated)
      // If an admin edited their OWN record, keep the top-bar name in sync too.
      if (isSelf.value) {
        auth.patchUser({ first_name: updated.first_name, last_name: updated.last_name })
      }
    } else {
      const updated = await updateProfile({
        first_name: form.firstName.trim(),
        last_name: form.lastName.trim(),
        national_insurance_number: orNull(form.nino),
        utr: orNull(form.utr),
        date_of_birth: orNull(form.dob),
        address_line_1: orNull(form.addr1),
        address_line_2: orNull(form.addr2),
        address_line_3: orNull(form.addr3),
        address_line_4: orNull(form.addr4),
        postcode: orNull(form.postcode),
      })
      hydrate(updated)
      // Keep the top-bar dropdown name in sync after a self rename.
      auth.patchUser({ first_name: updated.first_name, last_name: updated.last_name })
    }
    successMessage.value = 'The user details have been saved.'
  } catch (err) {
    // 401 is handled by apiFetch; 400/403/404/422 land here.
    formError.value = (err as ApiError)?.message ?? 'Could not save the changes. Please try again.'
  } finally {
    submitting.value = false
  }
}

// Cancel discards edits by re-applying the last loaded snapshots (profile + payroll).
function cancel() {
  if (snapshot.value) Object.assign(form, snapshot.value)
  if (payrollSnapshot.value) Object.assign(payroll, payrollSnapshot.value)
  for (const k of Object.keys(errors)) delete errors[k]
  formError.value = ''
  successMessage.value = ''
}

const pageTitle = computed(() =>
  isAdminMode.value ? `${form.firstName} ${form.lastName}`.trim() || 'User Details' : 'My Details',
)
</script>

<template>
  <AppLayout>
    <div class="mb-[18px] flex items-center justify-between gap-3">
      <h1 class="text-[22px] font-bold">{{ pageTitle }}</h1>
      <RouterLink
        v-if="isAdminMode"
        to="/users"
        class="text-sm font-semibold text-fa-green hover:underline"
      >
        ← Back to users
      </RouterLink>
    </div>

    <!-- Loading -->
    <FaCard v-if="loading" title="User details">
      <div class="py-10 text-center text-fa-muted">
        <i class="pi pi-spin pi-spinner mr-2" />Loading…
      </div>
    </FaCard>

    <!-- Load error -->
    <FaCard v-else-if="loadError" title="User details">
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

      <!-- 1. User details — two columns on wide screens (xl): the profile/payroll
           fields on the left, the personal address filling the former right-side
           whitespace. Below xl the grid collapses to one column (address stacks). -->
      <FaCard title="User details" note="Required fields *">
        <!-- Uneven split (not 50/50): the left fields nearly fill their half while
             the right address labels are right-aligned and sit well clear of the
             boundary, so an equal split leaves the divider hugging the left fields.
             A wider left column nudges the divider right to centre the whitespace
             between the two field groups. -->
        <div class="grid gap-x-10 xl:grid-cols-[1.1fr_0.9fr]">
          <!-- Left column: identity + payroll fields -->
          <div>
        <!-- Role: editable select in admin mode; read-only (the caller's own role)
             in self mode. -->
        <FormRow label="Role" label-for="role">
          <Select
            v-if="isAdminMode"
            id="role"
            v-model="form.role"
            :options="roleOptions"
            option-label="label"
            option-value="value"
            class="w-72"
          />
          <InputText
            v-else
            id="role"
            :value="roleOptions.find((r) => r.value === form.role)?.label ?? form.role"
            class="w-72"
            disabled
          />
        </FormRow>

        <FormRow label="Login / email" label-for="email">
          <InputText id="email" :value="email" class="w-full max-w-md" disabled />
          <p class="text-xs text-fa-muted">The login email can't be changed here.</p>
        </FormRow>

        <FormRow label="First name" label-for="first-name" required>
          <InputText
            id="first-name"
            v-model="form.firstName"
            class="w-72"
            :invalid="!!errors.firstName"
          />
          <p v-if="errors.firstName" class="text-xs text-[#c0392b]">{{ errors.firstName }}</p>
        </FormRow>

        <FormRow label="Last name" label-for="last-name" required>
          <InputText
            id="last-name"
            v-model="form.lastName"
            class="w-72"
            :invalid="!!errors.lastName"
          />
          <p v-if="errors.lastName" class="text-xs text-[#c0392b]">{{ errors.lastName }}</p>
        </FormRow>

        <FormRow label="National Insurance number" label-for="nino">
          <InputText id="nino" v-model="form.nino" class="w-72" placeholder="e.g. SY598539D" />
          <p class="text-xs text-fa-muted">
            Two letters, six digits and a final letter. Used by payroll.
          </p>
        </FormRow>

        <FormRow label="Unique Tax Reference" label-for="utr">
          <InputText id="utr" v-model="form.utr" class="w-72" placeholder="10-digit number" />
          <p class="text-xs text-fa-muted">Your personal 10-digit HMRC UTR (Self Assessment).</p>
        </FormRow>

        <FormRow label="Date of birth" label-for="dob">
          <InputText id="dob" v-model="form.dob" type="date" class="w-72" />
        </FormRow>

        <!-- Status: admin mode only. -->
        <FormRow v-if="isAdminMode" label="Status" label-for="status">
          <Select
            id="status"
            v-model="form.status"
            :options="statusOptions"
            option-label="label"
            option-value="value"
            class="w-72"
          />
        </FormRow>
          </div>

          <!-- Right column: personal/home address (payroll). All optional. On xl a
               left divider separates it from the identity fields. The address rows
               use their OWN narrower, LEFT-aligned label (not the shared FormRow's
               190px right-aligned one) so the fields sit close to the divider rather
               than floating off to the right. -->
          <div class="mt-2 xl:mt-0 xl:border-l xl:border-fa-border xl:pl-5">
            <div
              v-for="row in [
                { id: 'addr1', label: 'Address line 1', model: 'addr1' },
                { id: 'addr2', label: 'Address line 2', model: 'addr2' },
                { id: 'addr3', label: 'Address line 3', model: 'addr3' },
                { id: 'addr4', label: 'Address line 4', model: 'addr4' },
                { id: 'postcode', label: 'Postcode', model: 'postcode' },
              ]"
              :key="row.id"
              class="grid grid-cols-1 gap-1.5 py-2 sm:grid-cols-[120px_minmax(0,1fr)] sm:items-center sm:gap-4"
            >
              <label :for="row.id" class="text-sm text-fa-text">{{ row.label }}</label>
              <InputText
                :id="row.id"
                v-model="form[row.model as 'addr1' | 'addr2' | 'addr3' | 'addr4' | 'postcode']"
                :class="row.id === 'postcode' ? 'w-40' : 'w-full max-w-sm'"
              />
            </div>

            <!-- Email receipts inbox — shown here, in the whitespace under the
                 address, instead of a separate card buried below the payroll
                 sections. Same label-left row style as the address rows. -->
            <div
              v-if="inboxEnabled"
              class="mt-4 grid grid-cols-1 gap-1.5 border-t border-fa-border pt-4 sm:grid-cols-[120px_minmax(0,1fr)] sm:items-start sm:gap-4"
            >
              <label for="inbox-address" class="text-sm text-fa-text sm:pt-2">Email receipts</label>
              <div class="flex min-w-0 flex-col gap-1.5">
                <div class="flex w-full max-w-sm items-center gap-2">
                  <InputText id="inbox-address" :value="inboxAddress" class="min-w-0 flex-1" readonly />
                  <Button
                    :label="copied ? 'Copied' : 'Copy'"
                    :icon="copied ? 'pi pi-check' : 'pi pi-copy'"
                    severity="secondary"
                    outlined
                    @click="copyInbox"
                  />
                </div>
                <p class="text-xs text-fa-muted">
                  {{
                    isSelf
                      ? 'Forward receipts to this address to create draft expenses automatically.'
                      : 'Receipts forwarded to this address become draft expenses for this user.'
                  }}
                </p>
              </div>
            </div>
          </div>
        </div>
      </FaCard>

      <!-- Payroll sections — OWNER/ADMIN ONLY (isAdminMode requires isOrgAdmin and a
           /users/:id route). Members never reach these; the backend gates them too. -->
      <template v-if="isAdminMode">
        <!-- Employment details -->
        <FaCard title="Employment details">
          <FormRow label="Already on your payroll?" label-for="pay-existing">
            <Select id="pay-existing" v-model="payroll.is_existing_employee" :options="onPayrollOptions"
              option-label="label" option-value="value" class="w-full max-w-md" />
          </FormRow>
          <FormRow label="Employee start date" label-for="pay-start">
            <InputText id="pay-start" v-model="payroll.start_date" type="date" class="w-48" />
          </FormRow>
          <FormRow label="Starting declaration" label-for="pay-decl">
            <Select id="pay-decl" v-model="payroll.starting_declaration" :options="startingDeclarationOptions"
              option-label="label" option-value="value" class="w-full max-w-md" placeholder="Select…" showClear />
          </FormRow>
          <FormRow label="NICs calculated as" label-for="pay-nic">
            <Select id="pay-nic" v-model="payroll.nic_calculation" :options="nicCalculationOptions"
              option-label="label" option-value="value" class="w-full max-w-md" />
          </FormRow>
          <FormRow label="Normal working hours" label-for="pay-hours">
            <Select id="pay-hours" v-model="payroll.normal_working_hours" :options="workingHoursOptions"
              option-label="label" option-value="value" class="w-full max-w-md" placeholder="Select…" showClear />
          </FormRow>
          <FormRow label="Employee paid hourly?" label-for="pay-hourly">
            <Select id="pay-hourly" v-model="payroll.paid_hourly" :options="yesNo"
              option-label="label" option-value="value" class="w-40" />
          </FormRow>
          <FormRow label="Employee paid irregularly?" label-for="pay-irreg">
            <Select id="pay-irreg" v-model="payroll.paid_irregularly" :options="yesNo"
              option-label="label" option-value="value" class="w-40" />
          </FormRow>
          <FormRow label="Payroll ID" label-for="pay-id">
            <InputText id="pay-id" v-model="payroll.payroll_id" class="w-72" />
          </FormRow>
        </FaCard>

        <!-- Tax and National Insurance -->
        <FaCard title="Tax and National Insurance">
          <FormRow label="Tax code" label-for="pay-taxcode">
            <InputText id="pay-taxcode" v-model="payroll.tax_code" class="w-40" placeholder="e.g. 1257L" />
          </FormRow>
          <FormRow label="Week 1 / Month 1 basis?" label-for="pay-w1m1">
            <Select id="pay-w1m1" v-model="payroll.week1_month1_basis" :options="yesNo"
              option-label="label" option-value="value" class="w-40" />
          </FormRow>
          <FormRow label="NI category letter" label-for="pay-niletter">
            <Select id="pay-niletter" v-model="payroll.ni_category_letter" :options="niLetterOptions"
              option-label="label" option-value="value" class="w-28" />
          </FormRow>
          <FormRow label="Deduct student loans?">
            <div class="flex items-center gap-2">
              <Checkbox v-model="payroll.student_loan_undergraduate" inputId="pay-sl-ug" :binary="true" />
              <label for="pay-sl-ug" class="text-sm">Undergraduate loan</label>
            </div>
            <div class="flex items-center gap-2">
              <Checkbox v-model="payroll.student_loan_postgraduate" inputId="pay-sl-pg" :binary="true" />
              <label for="pay-sl-pg" class="text-sm">Postgraduate loan</label>
            </div>
          </FormRow>
        </FaCard>

        <!-- Monthly Pay -->
        <FaCard title="Monthly Pay">
          <FormRow v-for="f in monthlyPayFields" :key="f.key" :label="f.label" :label-for="`pay-${f.key}`">
            <InputText :id="`pay-${f.key}`" v-model="payroll[f.key]" class="w-40" inputmode="decimal" />
          </FormRow>
        </FaCard>

        <!-- Statutory Pay (Yes is deactivated — amount detail deferred) -->
        <FaCard title="Statutory Pay">
          <FormRow label="Receiving Statutory Pay?" label-for="pay-statutory">
            <Select id="pay-statutory" v-model="payroll.receiving_statutory_pay" :options="statutoryPayOptions"
              option-label="label" option-value="value" option-disabled="disabled" class="w-40" />
            <p class="text-xs text-fa-muted">Entering statutory payment amounts is coming soon.</p>
          </FormRow>
        </FaCard>

        <!-- Monthly Deductions -->
        <FaCard title="Monthly Deductions">
          <FormRow v-for="f in monthlyDeductionFields" :key="f.key" :label="f.label" :label-for="`pay-${f.key}`">
            <InputText :id="`pay-${f.key}`" v-model="payroll[f.key]" class="w-40" inputmode="decimal" />
          </FormRow>
        </FaCard>

        <!-- Pension (making contributions is deactivated — amount detail deferred) -->
        <FaCard title="Pension contributions">
          <FormRow label="Making pension contributions?" label-for="pay-pension">
            <Select id="pay-pension" v-model="payroll.pension_status" :options="pensionOptions"
              option-label="label" option-value="value" option-disabled="disabled" class="w-full max-w-md" />
            <p class="text-xs text-fa-muted">Entering contribution amounts is coming soon.</p>
          </FormRow>
        </FaCard>

        <!-- Leaving details -->
        <FaCard title="Leaving details">
          <FormRow label="Leaving during the next pay run?" label-for="pay-leaving">
            <Select id="pay-leaving" v-model="payroll.leaving_next_pay_run" :options="yesNo"
              option-label="label" option-value="value" class="w-40" />
          </FormRow>
          <FormRow v-if="payroll.leaving_next_pay_run" label="Leave date" label-for="pay-leavedate">
            <InputText id="pay-leavedate" v-model="payroll.leaving_date" type="date" class="w-48" />
          </FormRow>
        </FaCard>
      </template>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button label="Save changes" :loading="submitting" @click="submit" />
        <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
