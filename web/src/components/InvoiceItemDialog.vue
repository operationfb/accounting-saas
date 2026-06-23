<script setup lang="ts">
// The "New / Edit invoice item" modal (PrimeVue Dialog), modelled on the FreeAgent
// screen. It is a CONTROLLED, dumb component: it collects the line fields and emits
// an InvoiceItemRequest; the PARENT (InvoiceDetailView) owns the items array and
// the PUT that rebuilds the invoice's lines.
//
//   - create mode (editItem null): "Create and finish" / "Create and add another"
//     (keeps the dialog open and clears the line) / "Cancel".
//   - edit mode  (editItem set):   "Save changes" / "Cancel".
//
// VAT options come from the parent (built from the org's vat-rates); each option's
// VALUE is the percentage string the invoice API wants for sales_tax_rate ("20").
// Units and "add to price list" from the FreeAgent screen are omitted — no backend
// field for them yet.
import { reactive, watch } from 'vue'
import Dialog from 'primevue/dialog'
import InputText from 'primevue/inputtext'
import Textarea from 'primevue/textarea'
import Select from 'primevue/select'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import type { InvoiceItemRequest } from '@/types/invoice'

interface VatOption {
  label: string
  value: string
}

const props = defineProps<{
  visible: boolean
  vatOptions: VatOption[]
  // When set, the dialog is in EDIT mode (prefilled); null/undefined → CREATE mode.
  editItem?: InvoiceItemRequest | null
  // Currency symbol shown on the unit-price addon (defaults to £).
  currencySymbol?: string
  // True while the parent's PUT is in flight — drives the button spinners.
  saving?: boolean
}>()

const emit = defineEmits<{
  (e: 'update:visible', v: boolean): void
  (e: 'save', payload: { item: InvoiceItemRequest; addAnother: boolean }): void
}>()

const form = reactive({
  quantity: '1',
  description: '',
  unitPrice: '',
  vatRate: '0',
})
const errors = reactive<Record<string, string>>({})

// Prefer a 20% rate as the default, else the first available option, else "0".
function defaultVat(): string {
  return props.vatOptions.find((o) => o.value === '20')?.value ?? props.vatOptions[0]?.value ?? '0'
}

function resetForm() {
  for (const k of Object.keys(errors)) delete errors[k]
  if (props.editItem) {
    form.quantity = props.editItem.quantity
    form.description = props.editItem.description
    form.unitPrice = props.editItem.price
    form.vatRate = props.editItem.sales_tax_rate
  } else {
    form.quantity = '1'
    form.description = ''
    form.unitPrice = ''
    form.vatRate = defaultVat()
  }
}

// Reset every time the dialog opens (reads editItem at fire time, so add vs edit
// is decided correctly).
watch(
  () => props.visible,
  (v) => {
    if (v) resetForm()
  },
)

function validate(): boolean {
  for (const k of Object.keys(errors)) delete errors[k]
  if (form.description.trim() === '') errors.description = 'Enter a description.'
  if (form.quantity.trim() === '' || Number.isNaN(Number(form.quantity)))
    errors.quantity = 'Enter a quantity.'
  if (form.unitPrice.trim() === '' || Number.isNaN(Number(form.unitPrice)))
    errors.unitPrice = 'Enter a unit price.'
  return Object.keys(errors).length === 0
}

function buildItem(): InvoiceItemRequest {
  return {
    description: form.description.trim(),
    quantity: form.quantity.trim(),
    price: form.unitPrice.trim(),
    sales_tax_rate: form.vatRate,
  }
}

function submit(addAnother: boolean) {
  if (!validate()) return
  emit('save', { item: buildItem(), addAnother })
  if (addAnother) {
    // Keep the dialog open for the next line; clear the line-specific fields.
    form.description = ''
    form.unitPrice = ''
    form.quantity = '1'
  }
}
</script>

<template>
  <Dialog
    :visible="visible"
    modal
    :header="editItem ? 'Edit invoice item' : 'New invoice item'"
    :style="{ width: '40rem' }"
    :closable="!saving"
    @update:visible="(v) => emit('update:visible', v)"
  >
    <div class="flex flex-col gap-4">
      <div class="w-40">
        <label class="mb-1 block text-sm font-semibold text-fa-text">
          Quantity <span class="text-fa-required">*</span>
        </label>
        <InputText v-model="form.quantity" inputmode="decimal" class="w-full" :invalid="!!errors.quantity" />
        <p v-if="errors.quantity" class="mt-1 text-xs text-[#c0392b]">{{ errors.quantity }}</p>
      </div>

      <div>
        <label class="mb-1 block text-sm font-semibold text-fa-text">
          Details <span class="text-fa-required">*</span>
        </label>
        <Textarea v-model="form.description" rows="3" class="w-full" :invalid="!!errors.description" />
        <p v-if="errors.description" class="mt-1 text-xs text-[#c0392b]">{{ errors.description }}</p>
      </div>

      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div>
          <label class="mb-1 block text-sm font-semibold text-fa-text">
            Unit price <span class="text-fa-required">*</span>
          </label>
          <InputGroup>
            <InputGroupAddon>{{ currencySymbol || '£' }}</InputGroupAddon>
            <InputText v-model="form.unitPrice" inputmode="decimal" :invalid="!!errors.unitPrice" />
          </InputGroup>
          <p class="mt-1 text-xs text-fa-muted">Enter discounts and credits as negative.</p>
          <p v-if="errors.unitPrice" class="mt-1 text-xs text-[#c0392b]">{{ errors.unitPrice }}</p>
        </div>
        <div>
          <label class="mb-1 block text-sm font-semibold text-fa-text">VAT</label>
          <Select
            v-model="form.vatRate"
            :options="vatOptions"
            option-label="label"
            option-value="value"
            class="w-full"
          />
        </div>
      </div>
    </div>

    <template #footer>
      <!-- Edit mode: Save / Cancel. -->
      <template v-if="editItem">
        <button
          type="button"
          class="mr-3 font-semibold text-fa-blue hover:underline disabled:opacity-50"
          :disabled="saving"
          @click="emit('update:visible', false)"
        >
          Cancel
        </button>
        <Button label="Save changes" :loading="saving" @click="submit(false)" />
      </template>
      <!-- Create mode: finish / add-another / Cancel. -->
      <template v-else>
        <Button label="Create and finish" :loading="saving" @click="submit(false)" />
        <Button
          label="Create and add another"
          severity="secondary"
          outlined
          :loading="saving"
          @click="submit(true)"
        />
        <button
          type="button"
          class="ml-3 font-semibold text-fa-blue hover:underline disabled:opacity-50"
          :disabled="saving"
          @click="emit('update:visible', false)"
        >
          Cancel
        </button>
      </template>
    </template>
  </Dialog>
</template>
