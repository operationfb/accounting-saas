<script setup lang="ts">
// New expense form — the "expense entry" screen, modelled on FreeAgent's
// "New Out-of-Pocket Expense". STATIC scaffolding: fields render but nothing is
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
    <h1 class="page-title">New Out-of-Pocket Expense</h1>

    <FaCard title="Attachment">
      <div class="frow">
        <label class="frow__label">File to attach</label>
        <div class="frow__control frow__control--inline">
          <Button label="Upload a file" severity="secondary" outlined />
          <span class="frow__or">or</span>
          <a href="#" class="fa-link"><i class="pi pi-bolt" /> Upload via Smart Capture</a>
        </div>
      </div>
      <div class="frow">
        <label class="frow__label" for="att-desc">Attachment description</label>
        <div class="frow__control">
          <InputText id="att-desc" v-model="attachmentDesc" class="w-72" />
        </div>
      </div>
    </FaCard>

    <FaCard title="Expense details" note="Required fields *">
      <div class="frow">
        <label class="frow__label" for="category">Category<span class="fa-required">*</span></label>
        <div class="frow__control">
          <Select id="category" v-model="category" :options="categoryOptions" placeholder="Select a category" class="w-72" />
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="dated">Dated<span class="fa-required">*</span></label>
        <div class="frow__control">
          <DatePicker id="dated" v-model="datedOn" date-format="dd M yy" show-icon :show-on-focus="false" />
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="currency">Currency<span class="fa-required">*</span></label>
        <div class="frow__control">
          <Select id="currency" v-model="currency" :options="currencyOptions" class="w-40" />
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="total">Total value<span class="fa-required">*</span></label>
        <div class="frow__control">
          <!-- Money is entered as text (never a numeric/float input) and prefixed
               with the currency symbol, matching FreeAgent. -->
          <InputGroup class="w-56">
            <InputGroupAddon>£</InputGroupAddon>
            <InputText id="total" v-model="totalValue" placeholder="0.00" inputmode="decimal" />
          </InputGroup>
        </div>
      </div>

      <div class="frow">
        <label class="frow__label">VAT options</label>
        <div class="frow__control frow__control--stack">
          <label class="radio"><RadioButton v-model="vatOption" value="uk" input-id="vat-uk" /><span>UK VAT Rates</span></label>
          <label class="radio"><RadioButton v-model="vatOption" value="reverse" input-id="vat-rev" /><span>Reverse Charge</span></label>
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="vatrate">VAT rate</label>
        <div class="frow__control">
          <Select id="vatrate" v-model="vatRate" :options="vatRateOptions" class="w-56" />
          <p class="frow__help">
            Enter any amount which can be reclaimed from HMRC on your VAT return.
            <a href="#" class="fa-link">Find out more in our knowledge base</a>.
          </p>
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="description">Description<span class="fa-required">*</span></label>
        <div class="frow__control">
          <InputText id="description" v-model="description" class="w-full max-w-xl" />
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="supplier">Supplier name</label>
        <div class="frow__control">
          <InputText id="supplier" v-model="supplierName" class="w-72" />
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="supplier-vat">Supplier VAT number</label>
        <div class="frow__control">
          <InputText id="supplier-vat" v-model="supplierVat" class="w-56" />
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="invoice">Invoice number</label>
        <div class="frow__control">
          <InputText id="invoice" v-model="invoiceNumber" class="w-56" />
        </div>
      </div>

      <div class="frow">
        <label class="frow__label" for="receipt">Receipt reference</label>
        <div class="frow__control">
          <InputText id="receipt" v-model="receiptReference" class="w-40" />
        </div>
      </div>
    </FaCard>

    <FaCard title="Is this a project expense?">
      <div class="frow">
        <label class="frow__label" for="project">Link to project</label>
        <div class="frow__control">
          <Select id="project" v-model="project" :options="projectOptions" class="w-72" />
        </div>
      </div>
    </FaCard>

    <FaCard title="Recurring options">
      <div class="frow">
        <label class="frow__label" for="recurs">This expense recurs</label>
        <div class="frow__control">
          <Select id="recurs" v-model="recurrence" :options="recurrenceOptions" class="w-72" />
          <p class="frow__help">
            We'll create a duplicate of this expense after the period you specify.
            To recur forever, leave the end date blank.
          </p>
        </div>
      </div>
    </FaCard>

    <div class="form-actions">
      <Button label="Create new expense" />
      <Button label="Create and add another" severity="secondary" outlined />
      <a href="#" class="fa-link form-actions__cancel">Cancel</a>
    </div>
  </AppLayout>
</template>

<style scoped>
.page-title {
  margin: 0 0 18px;
  font-size: 22px;
  font-weight: 700;
}

/* Two-column form row: right-aligned label, left-aligned control. */
.frow {
  display: grid;
  grid-template-columns: 190px minmax(0, 1fr);
  gap: 16px;
  align-items: center;
  padding: 8px 0;
}
.frow__label {
  text-align: right;
  font-size: 14px;
  color: var(--fa-text);
}
.frow__control {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.frow__control--inline {
  flex-direction: row;
  align-items: center;
  gap: 10px;
}
.frow__control--stack {
  gap: 8px;
}
.frow__or {
  color: var(--fa-muted);
  font-size: 14px;
}
.frow__help {
  margin: 0;
  font-size: 12px;
  color: var(--fa-muted);
  max-width: 32rem;
}
.radio {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  cursor: pointer;
}

.form-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 0 24px;
}
.form-actions__cancel {
  font-weight: 600;
}

/* On narrow screens, stack the label above the control. */
@media (max-width: 640px) {
  .frow {
    grid-template-columns: 1fr;
    gap: 6px;
  }
  .frow__label {
    text-align: left;
  }
}
</style>
