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
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'
import { useAuthStore } from '@/stores/auth'
import { getProfile, updateProfile, getInboxAddress } from '@/services/user.service'
import { getMember, updateMember, getMemberInboxAddress } from '@/services/members.service'
import type { ApiError } from '@/lib/api'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

// --- mode ---
// The optional :id path param. Absent (/my-details) or equal to the caller →
// self mode; a different id → admin mode (edit another user).
const targetId = computed(() => (route.params.id as string | undefined) || undefined)
const isSelf = computed(() => !targetId.value || targetId.value === auth.user?.id)
const isAdminMode = computed(() => !isSelf.value)

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
  // Guard: a non-admin can't edit someone else — bounce to their own details.
  if (isAdminMode.value && !auth.isOrgAdmin) {
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
      })
      hydrate(updated)
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

// Cancel discards edits by re-applying the last loaded snapshot.
function cancel() {
  if (snapshot.value) Object.assign(form, snapshot.value)
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
          </div>
        </div>
      </FaCard>

      <!-- 2. Email receipts (both modes, when the inbox channel is enabled) -->
      <FaCard v-if="inboxEnabled" title="Email receipts">
        <p class="mb-2 text-sm text-fa-muted">
          {{
            isSelf
              ? "Forward receipts to this address and they'll become draft expenses automatically."
              : "Receipts forwarded to this address become draft expenses for this user."
          }}
        </p>
        <FormRow :label="isSelf ? 'Your inbox address' : 'Inbox address'" label-for="inbox-address">
          <div class="flex w-full max-w-md items-center gap-2">
            <InputText id="inbox-address" :value="inboxAddress" class="min-w-0 flex-1" readonly />
            <Button
              :label="copied ? 'Copied' : 'Copy'"
              :icon="copied ? 'pi pi-check' : 'pi pi-copy'"
              severity="secondary"
              outlined
              @click="copyInbox"
            />
          </div>
        </FormRow>
      </FaCard>

      <div class="flex items-center gap-3 py-2 pb-6">
        <Button label="Save changes" :loading="submitting" @click="submit" />
        <button type="button" class="font-semibold text-fa-green hover:underline" @click="cancel">
          Cancel
        </button>
      </div>
    </template>
  </AppLayout>
</template>
