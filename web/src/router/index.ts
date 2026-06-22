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
import BankAccountListView from '@/views/BankAccountListView.vue'
import BankAccountEntryView from '@/views/BankAccountEntryView.vue'
import BankAccountTransactionsView from '@/views/BankAccountTransactionsView.vue'
import BankTransactionEntryView from '@/views/BankTransactionEntryView.vue'
import BankStatementImportView from '@/views/BankStatementImportView.vue'
import CompanyDetailsView from '@/views/CompanyDetailsView.vue'
import MyDetailsView from '@/views/MyDetailsView.vue'
import IntegrationsView from '@/views/IntegrationsView.vue'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: '/expenses' },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/forgot', name: 'forgot', component: ForgotPasswordView },
    // Reached from the reset email link, which carries the one-time token as a
    // PATH segment (the backend builds {APP_BASE_URL}/reset-password/<token>).
    // ResetPasswordView reads it from route.params.token.
    { path: '/reset-password/:token', name: 'reset-password', component: ResetPasswordView },
    { path: '/contacts', name: 'contacts', component: ContactListView, meta: { requiresAuth: true, title: 'Contacts' } },
    { path: '/contacts/new', name: 'contact-new', component: ContactEntryView, meta: { requiresAuth: true, title: 'Contacts' } },
    { path: '/contacts/:id/edit', name: 'contact-edit', component: ContactEntryView, meta: { requiresAuth: true, title: 'Contacts' } },
    { path: '/projects', name: 'projects', component: ProjectListView, meta: { requiresAuth: true, title: 'Projects' } },
    { path: '/projects/new', name: 'project-new', component: ProjectEntryView, meta: { requiresAuth: true, title: 'Projects' } },
    { path: '/projects/:id/edit', name: 'project-edit', component: ProjectEntryView, meta: { requiresAuth: true, title: 'Projects' } },
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
    // The organisation's own "Company Details" — a singleton settings screen
    // (the org comes from the token, so there's no id in the path).
    { path: '/company-details', name: 'company-details', component: CompanyDetailsView, meta: { requiresAuth: true, title: 'Settings' } },
    // The signed-in user's own "My Details" — likewise a singleton (the user
    // comes from the token). Every user may edit their own profile.
    { path: '/my-details', name: 'my-details', component: MyDetailsView, meta: { requiresAuth: true, title: 'Settings' } },
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
    return { name: 'expenses' }
  }
  return true
})

export default router
