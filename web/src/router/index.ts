import { createRouter, createWebHistory } from 'vue-router'

import LoginView from '@/views/LoginView.vue'
import ExpenseListView from '@/views/ExpenseListView.vue'
import ExpenseEntryView from '@/views/ExpenseEntryView.vue'
import ExpenseDetailView from '@/views/ExpenseDetailView.vue'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: '/expenses' },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/expenses', name: 'expenses', component: ExpenseListView, meta: { requiresAuth: true } },
    { path: '/expenses/new', name: 'expense-new', component: ExpenseEntryView, meta: { requiresAuth: true } },
    { path: '/expenses/:id', name: 'expense-detail', component: ExpenseDetailView, meta: { requiresAuth: true } },
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
