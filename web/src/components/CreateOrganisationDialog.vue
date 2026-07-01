<script setup lang="ts">
// "Create Organisation" modal (PrimeVue Dialog), used by the superuser god view.
// Self-contained: it owns its form state, validates, calls the admin create API
// (which also provisions the new org's chart of accounts server-side), and emits
// `created` with the new org on success. The parent (AdminOrganisationsView) opens
// it and reacts to `created`.
//
// Deliberately minimal — name + the two creation-time immutable fields (country,
// native currency). company type, address and VAT are filled in afterward on the
// org's Company Details screen (both country and currency are immutable there).
import { reactive, ref, watch } from 'vue'
import Dialog from 'primevue/dialog'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import Button from 'primevue/button'
import FormRow from '@/components/FormRow.vue'
import { COUNTRIES } from '@/lib/countries'
import { buildCurrencyOptions, type CurrencyOption } from '@/lib/currency'
import { listCurrencies } from '@/services/currencies.service'
import { createAdminOrganisation } from '@/services/admin.service'
import type { AdminOrganisation } from '@/types/admin'
import type { ApiError } from '@/lib/api'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{
  'update:visible': [value: boolean]
  created: [org: AdminOrganisation]
}>()

const countryOptions = COUNTRIES.map((c) => ({ label: c.name, value: c.code }))
const currencyOptions = ref<CurrencyOption[]>([])

const defaults = () => ({ name: '', countryCode: 'GB', nativeCurrency: 'GBP' })
const form = reactive(defaults())

const errors = reactive<Record<string, string>>({})
const submitting = ref(false)
const formError = ref('')

// Reset + load the currency list each time the dialog opens.
watch(
  () => props.visible,
  async (open) => {
    if (!open) return
    Object.assign(form, defaults())
    for (const k of Object.keys(errors)) delete errors[k]
    formError.value = ''
    if (currencyOptions.value.length === 0) {
      currencyOptions.value = buildCurrencyOptions(await listCurrencies().catch(() => []))
    }
  },
)

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (form.name.trim() === '') errors.name = 'Enter a company name.'
  if (!form.countryCode) errors.countryCode = 'Pick a country.'
  if (!form.nativeCurrency) errors.nativeCurrency = 'Pick a currency.'
  return Object.keys(errors).length === 0
}

function close() {
  emit('update:visible', false)
}

async function submit() {
  if (submitting.value) return
  formError.value = ''
  if (!validate()) return
  submitting.value = true
  try {
    const org = await createAdminOrganisation({
      name: form.name.trim(),
      country_code: form.countryCode,
      native_currency: form.nativeCurrency,
    })
    emit('created', org)
    close()
  } catch (err) {
    // 409 (name/slug clash), 422 (unknown currency), etc. land here.
    formError.value = (err as ApiError)?.message ?? 'Could not create the organisation.'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Dialog
    :visible="visible"
    modal
    header="Create Organisation"
    :style="{ width: '38rem' }"
    :closable="!submitting"
    @update:visible="(v: boolean) => emit('update:visible', v)"
  >
    <div
      v-if="formError"
      class="mb-4 rounded border border-[#f6d3d0] bg-[#fdecec] px-3 py-2 text-sm text-[#c0392b]"
      role="alert"
    >
      {{ formError }}
    </div>

    <p class="mb-4 text-sm text-fa-muted">
      Country and native currency are fixed at creation and can't be changed later. A default
      chart of accounts is created automatically.
    </p>

    <FormRow label="Company name" label-for="new-org-name" required>
      <InputText
        id="new-org-name"
        v-model="form.name"
        class="w-full"
        :invalid="!!errors.name"
        @keyup.enter="submit"
      />
      <p v-if="errors.name" class="text-xs text-[#c0392b]">{{ errors.name }}</p>
    </FormRow>

    <FormRow label="Country" label-for="new-org-country" required>
      <Select
        id="new-org-country"
        v-model="form.countryCode"
        :options="countryOptions"
        option-label="label"
        option-value="value"
        filter
        filter-placeholder="Search countries"
        class="w-full sm:w-72"
        :invalid="!!errors.countryCode"
      />
    </FormRow>

    <FormRow label="Native currency" label-for="new-org-currency" required>
      <Select
        id="new-org-currency"
        v-model="form.nativeCurrency"
        :options="currencyOptions"
        option-label="label"
        option-value="value"
        filter
        filter-placeholder="Search currencies"
        class="w-full sm:w-72"
        :invalid="!!errors.nativeCurrency"
      />
    </FormRow>

    <template #footer>
      <Button label="Cancel" severity="secondary" outlined :disabled="submitting" @click="close" />
      <Button label="Create Organisation" :loading="submitting" @click="submit" />
    </template>
  </Dialog>
</template>
