import { createRouter, createWebHistory } from 'vue-router'

import LoginView from '@/views/LoginView.vue'
import ForgotPasswordView from '@/views/ForgotPasswordView.vue'
import ResetPasswordView from '@/views/ResetPasswordView.vue'
import ExpenseListView from '@/views/ExpenseListView.vue'
import ExpenseEntryView from '@/views/ExpenseEntryView.vue'
import ExpenseDetailView from '@/views/ExpenseDetailView.vue'
import ContactListView from '@/views/ContactListView.vue'
import ContactEntryView from '@/views/ContactEntryView.vue'
import ProjectListView from '@/views/ProjectListView.vue'
import ProjectEntryView from '@/views/ProjectEntryView.vue'
import InvoiceListView from '@/views/InvoiceListView.vue'
import InvoiceEntryView from '@/views/InvoiceEntryView.vue'
import InvoiceDetailView from '@/views/InvoiceDetailView.vue'
import BillListView from '@/views/BillListView.vue'
import BillEntryView from '@/views/BillEntryView.vue'
import BankAccountListView from '@/views/BankAccountListView.vue'
import BankAccountEntryView from '@/views/BankAccountEntryView.vue'
import BankAccountTransactionsView from '@/views/BankAccountTransactionsView.vue'
import BankTransactionEntryView from '@/views/BankTransactionEntryView.vue'
import BankStatementImportView from '@/views/BankStatementImportView.vue'
import CompanyDetailsView from '@/views/CompanyDetailsView.vue'
import VatSettingsView from '@/views/VatSettingsView.vue'
import VatReturnListView from '@/views/VatReturnListView.vue'
import VatReturnDetailView from '@/views/VatReturnDetailView.vue'
import OverviewDashboardView from '@/views/OverviewDashboardView.vue'
import MyDetailsView from '@/views/MyDetailsView.vue'
import UsersListView from '@/views/UsersListView.vue'
import IntegrationsView from '@/views/IntegrationsView.vue'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/forgot', name: 'forgot', component: ForgotPasswordView },
    // Reached from the reset email link, which carries the one-time token as a
    // PATH segment (the backend builds {APP_BASE_URL}/reset-password/<token>).
    // ResetPasswordView reads it from route.params.token.
    { path: '/reset-password/:token', name: 'reset-password', component: ResetPasswordView },
    // The "Overview" landing — a tabbed dashboard: the new financial Overview
    // (placeholder for now) + the existing HMRC MTD VAT dashboard as a 2nd tab.
    // First item in the top nav.
    { path: '/overview', name: 'overview', component: OverviewDashboardView, meta: { requiresAuth: true, title: 'Overview' } },
    { path: '/contacts', name: 'contacts', component: ContactListView, meta: { requiresAuth: true, title: 'Contacts' } },
    { path: '/contacts/new', name: 'contact-new', component: ContactEntryView, meta: { requiresAuth: true, title: 'Contacts' } },
    { path: '/contacts/:id/edit', name: 'contact-edit', component: ContactEntryView, meta: { requiresAuth: true, title: 'Contacts' } },
    { path: '/projects', name: 'projects', component: ProjectListView, meta: { requiresAuth: true, title: 'Projects' } },
    { path: '/projects/new', name: 'project-new', component: ProjectEntryView, meta: { requiresAuth: true, title: 'Projects' } },
    { path: '/projects/:id/edit', name: 'project-edit', component: ProjectEntryView, meta: { requiresAuth: true, title: 'Projects' } },
    // Invoices. /invoices/new is declared BEFORE /invoices/:id so "new" isn't
    // captured as an id (vue-router matches in declaration order).
    { path: '/invoices', name: 'invoices', component: InvoiceListView, meta: { requiresAuth: true, title: 'Invoices' } },
    { path: '/invoices/new', name: 'invoice-new', component: InvoiceEntryView, meta: { requiresAuth: true, title: 'Invoices' } },
    { path: '/invoices/:id', name: 'invoice-detail', component: InvoiceDetailView, meta: { requiresAuth: true, title: 'Invoices' } },
    { path: '/invoices/:id/edit', name: 'invoice-edit', component: InvoiceEntryView, meta: { requiresAuth: true, title: 'Invoices' } },
    // Bills (accounts payable). Two views: the list + a dual-mode create/edit form
    // (no read-only detail). /bills/new is declared BEFORE /bills/:id/edit.
    { path: '/bills', name: 'bills', component: BillListView, meta: { requiresAuth: true, title: 'Bills' } },
    { path: '/bills/new', name: 'bill-new', component: BillEntryView, meta: { requiresAuth: true, title: 'Bills' } },
    { path: '/bills/:id/edit', name: 'bill-edit', component: BillEntryView, meta: { requiresAuth: true, title: 'Bills' } },
    { path: '/bank-accounts', name: 'bank-accounts', component: BankAccountListView, meta: { requiresAuth: true, title: 'Banking' } },
    { path: '/bank-accounts/new', name: 'bank-account-new', component: BankAccountEntryView, meta: { requiresAuth: true, title: 'Banking' } },
    { path: '/bank-accounts/:id', name: 'bank-account-transactions', component: BankAccountTransactionsView, meta: { requiresAuth: true, title: 'Banking' } },
    { path: '/bank-accounts/:id/edit', name: 'bank-account-edit', component: BankAccountEntryView, meta: { requiresAuth: true, title: 'Banking' } },
    { path: '/bank-accounts/:id/transactions/new', name: 'bank-transaction-new', component: BankTransactionEntryView, meta: { requiresAuth: true, title: 'Banking' } },
    { path: '/bank-accounts/:id/transactions/import', name: 'bank-statement-import', component: BankStatementImportView, meta: { requiresAuth: true, title: 'Banking' } },
    { path: '/bank-accounts/:id/transactions/:txnId/edit', name: 'bank-transaction-edit', component: BankTransactionEntryView, meta: { requiresAuth: true, title: 'Banking' } },
    { path: '/expenses', name: 'expenses', component: ExpenseListView, meta: { requiresAuth: true, title: 'Expenses' } },
    { path: '/expenses/new', name: 'expense-new', component: ExpenseEntryView, meta: { requiresAuth: true, title: 'Expenses' } },
    { path: '/expenses/:id', name: 'expense-detail', component: ExpenseDetailView, meta: { requiresAuth: true, title: 'Expenses' } },
    { path: '/expenses/:id/edit', name: 'expense-edit', component: ExpenseEntryView, meta: { requiresAuth: true, title: 'Expenses' } },
    // VAT returns: the list of return periods generated from the org's VAT settings.
    // The per-period detail (Preview / Full Report) is a later slice.
    { path: '/vat-returns', name: 'vat-returns', component: VatReturnListView, meta: { requiresAuth: true, title: 'VAT' } },
    // Per-period return detail (Preview + Full Report). :periodKey is the period-end date.
    { path: '/vat-returns/:periodKey', name: 'vat-return-detail', component: VatReturnDetailView, meta: { requiresAuth: true, title: 'VAT' } },
    // The organisation's own "Company Details" — a singleton settings screen
    // (the org comes from the token, so there's no id in the path).
    { path: '/company-details', name: 'company-details', component: CompanyDetailsView, meta: { requiresAuth: true, title: 'Settings' } },
    // The organisation's VAT registration settings — likewise a singleton (the org
    // comes from the token). Read by any active member; edited by owner/admin.
    { path: '/vat-registration', name: 'vat-registration', component: VatSettingsView, meta: { requiresAuth: true, title: 'Settings' } },
    // The signed-in user's own "My Details" — the unified User Details view in
    // SELF mode (the user comes from the token). Every user may edit their own profile.
    { path: '/my-details', name: 'my-details', component: MyDetailsView, meta: { requiresAuth: true, title: 'Settings' } },
    // Users management. The list is owner/admin-only (the view + the API both gate
    // it; a non-admin is redirected to their own details). /users/:id is the SAME
    // unified User Details view in ADMIN mode (or self mode if :id is the caller).
    { path: '/users', name: 'users', component: UsersListView, meta: { requiresAuth: true, title: 'Users' } },
    { path: '/users/:id', name: 'user-detail', component: MyDetailsView, meta: { requiresAuth: true, title: 'Users' } },
    // Integration settings (FreeAgent OAuth + status). The path is fixed: the
    // backend OAuth callback redirects the browser to /settings/integrations with
    // ?freeagent=connected | ?freeagent=error&reason=… (integration_service.go).
    { path: '/settings/integrations', name: 'integrations', component: IntegrationsView, meta: { requiresAuth: true, title: 'Settings' } },
  ],
})

// Update the browser tab title on every navigation.
router.afterEach((to) => {
  const module = to.meta.title as string | undefined
  document.title = module ? `Kontala ${module}` : 'Kontala'
})

// Auth guard:
//   - protected route + not authenticated → /login (remember where we wanted to go)
//   - already authenticated + heading to /login → send to the list instead
router.beforeEach((to) => {
  const auth = useAuthStore()
  if (to.meta.requiresAuth && !auth.isAuthenticated) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  if (to.name === 'login' && auth.isAuthenticated) {
    return { name: 'overview' }
  }
  return true
})

export default router
