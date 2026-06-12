<script setup lang="ts">
// New expense form — the "expense entry" screen, modelled on FA's "New
// Out-of-Pocket Expense". STATIC scaffolding: fields render but nothing is
// validated, converted or submitted. The local refs only exist so the PrimeVue
// controls have something to bind to.
import { ref } from 'vue'
import InputText from 'primevue/inputtext'
import Select from 'primevue/select'
import DatePicker from 'primevue/datepicker'
import RadioButton from 'primevue/radiobutton'
import InputGroup from 'primevue/inputgroup'
import InputGroupAddon from 'primevue/inputgroupaddon'
import Button from 'primevue/button'
import AppLayout from '@/layouts/AppLayout.vue'
import FaCard from '@/components/FaCard.vue'
import FormRow from '@/components/FormRow.vue'

// --- placeholder field state (no logic) ---
const attachmentDesc = ref('')

const category = ref()
const categoryOptions = ['Accommodation and Meals', 'Travel', 'Office Supplies', 'Computer Equipment']

const datedOn = ref()

const currency = ref('GBP')
const currencyOptions = ['GBP', 'EUR', 'USD']

const totalValue = ref('')

const vatRate = ref('Standard 20%')
const vatRateOptions = ['Standard 20%', 'Reduced 5%', 'Zero 0%', 'Exempt']

const vatOption = ref('uk')

const description = ref('')
const supplierName = ref('')
const supplierVat = ref('')
const invoiceNumber = ref('')
const receiptReference = ref('')

const project = ref('-- None --')
const projectOptions = ['-- None --', 'Website redesign', 'Client onboarding']

const recurrence = ref('-- Does Not Recur --')
const recurrenceOptions = ['-- Does Not Recur --', 'Weekly', 'Monthly', 'Quarterly', 'Yearly']
</script>

<template>
  <AppLayout>
    <h1 class="mb-[18px] text-[22px] font-bold">New Out-of-Pocket Expense</h1>

    <FaCard title="Attachment">
      <FormRow label="File to attach">
        <div class="flex items-center gap-2.5">
          <Button label="Upload a file" severity="secondary" outlined />
          <span class="text-sm text-fa-muted">or</span>
          <a href="#" class="text-fa-blue hover:underline"><i class="pi pi-bolt" /> Upload via Smart Capture</a>
        </div>
      </FormRow>
      <FormRow label="Attachment description" label-for="att-desc">
        <InputText id="att-desc" v-model="attachmentDesc" class="w-72" />
      </FormRow>
    </FaCard>

    <FaCard title="Expense details" note="Required fields *">
      <FormRow label="Category" label-for="category" required>
        <Select id="category" v-model="category" :options="categoryOptions" placeholder="Select a category" class="w-72" />
      </FormRow>

      <FormRow label="Dated" label-for="dated" required>
        <DatePicker id="dated" v-model="datedOn" date-format="dd M yy" show-icon :show-on-focus="false" />
      </FormRow>

      <FormRow label="Currency" label-for="currency" required>
        <Select id="currency" v-model="currency" :options="currencyOptions" class="w-40" />
      </FormRow>

      <FormRow label="Total value" label-for="total" required>
        <!-- Money is entered as text (never a numeric/float input) and prefixed
             with the currency symbol, matching FA. -->
        <InputGroup class="w-56">
          <InputGroupAddon>£</InputGroupAddon>
          <InputText id="total" v-model="totalValue" placeholder="0.00" inputmode="decimal" />
        </InputGroup>
      </FormRow>

      <FormRow label="VAT options">
        <label class="inline-flex cursor-pointer items-center gap-2 text-sm">
          <RadioButton v-model="vatOption" value="uk" input-id="vat-uk" /><span>UK VAT Rates</span>
        </label>
        <label class="inline-flex cursor-pointer items-center gap-2 text-sm">
          <RadioButton v-model="vatOption" value="reverse" input-id="vat-rev" /><span>Reverse Charge</span>
        </label>
      </FormRow>

      <FormRow label="VAT rate" label-for="vatrate">
        <Select id="vatrate" v-model="vatRate" :options="vatRateOptions" class="w-56" />
        <p class="max-w-[32rem] text-xs text-fa-muted">
          Enter any amount which can be reclaimed from HMRC on your VAT return.
          <a href="#" class="text-fa-blue hover:underline">Find out more in our knowledge base</a>.
        </p>
      </FormRow>

      <FormRow label="Description" label-for="description" required>
        <InputText id="description" v-model="description" class="w-full max-w-xl" />
      </FormRow>

      <FormRow label="Supplier name" label-for="supplier">
        <InputText id="supplier" v-model="supplierName" class="w-72" />
      </FormRow>

      <FormRow label="Supplier VAT number" label-for="supplier-vat">
        <InputText id="supplier-vat" v-model="supplierVat" class="w-56" />
      </FormRow>

      <FormRow label="Invoice number" label-for="invoice">
        <InputText id="invoice" v-model="invoiceNumber" class="w-56" />
      </FormRow>

      <FormRow label="Receipt reference" label-for="receipt">
        <InputText id="receipt" v-model="receiptReference" class="w-40" />
      </FormRow>
    </FaCard>

    <FaCard title="Is this a project expense?">
      <FormRow label="Link to project" label-for="project">
        <Select id="project" v-model="project" :options="projectOptions" class="w-72" />
      </FormRow>
    </FaCard>

    <FaCard title="Recurring options">
      <FormRow label="This expense recurs" label-for="recurs">
        <Select id="recurs" v-model="recurrence" :options="recurrenceOptions" class="w-72" />
        <p class="max-w-[32rem] text-xs text-fa-muted">
          We'll create a duplicate of this expense after the period you specify.
          To recur forever, leave the end date blank.
        </p>
      </FormRow>
    </FaCard>

    <div class="flex items-center gap-3 py-2 pb-6">
      <Button label="Create new expense" />
      <Button label="Create and add another" severity="secondary" outlined />
      <a href="#" class="font-semibold text-fa-blue hover:underline">Cancel</a>
    </div>
  </AppLayout>
</template>
