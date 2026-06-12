import { createRouter, createWebHistory } from 'vue-router'

import LoginView from '@/views/LoginView.vue'
import ExpenseListView from '@/views/ExpenseListView.vue'
import ExpenseEntryView from '@/views/ExpenseEntryView.vue'
import ExpenseDetailView from '@/views/ExpenseDetailView.vue'

// Scaffolding only: plain routes, NO auth guard yet. The navigation guard that
// redirects unauthenticated users to /login is added in a later (functional) step.
const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/', redirect: '/expenses' },
    { path: '/login', name: 'login', component: LoginView },
    { path: '/expenses', name: 'expenses', component: ExpenseListView },
    { path: '/expenses/new', name: 'expense-new', component: ExpenseEntryView },
    { path: '/expenses/:id', name: 'expense-detail', component: ExpenseDetailView },
  ],
})

export default router
