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
    { path: '/contacts', name: 'contacts', component: ContactListView, meta: { requiresAuth: true } },
    { path: '/contacts/new', name: 'contact-new', component: ContactEntryView, meta: { requiresAuth: true } },
    { path: '/contacts/:id/edit', name: 'contact-edit', component: ContactEntryView, meta: { requiresAuth: true } },
    { path: '/projects', name: 'projects', component: ProjectListView, meta: { requiresAuth: true } },
    { path: '/projects/new', name: 'project-new', component: ProjectEntryView, meta: { requiresAuth: true } },
    { path: '/projects/:id/edit', name: 'project-edit', component: ProjectEntryView, meta: { requiresAuth: true } },
    { path: '/expenses', name: 'expenses', component: ExpenseListView, meta: { requiresAuth: true } },
    { path: '/expenses/new', name: 'expense-new', component: ExpenseEntryView, meta: { requiresAuth: true } },
    { path: '/expenses/:id', name: 'expense-detail', component: ExpenseDetailView, meta: { requiresAuth: true } },
    { path: '/expenses/:id/edit', name: 'expense-edit', component: ExpenseEntryView, meta: { requiresAuth: true } },
  ],
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
