<script setup lang="ts">
// App shell used by the authenticated pages (list / entry / detail):
//   top navigation  →  page content (<slot/>)  →  footer
// LoginView does NOT use this — it is a standalone full-page screen.
import AppTopBar from '@/components/AppTopBar.vue'
import AppFooter from '@/components/AppFooter.vue'
</script>

<template>
  <div class="flex min-h-screen flex-col bg-white">
    <AppTopBar />
    <main class="flex-1">
      <div class="mx-auto max-w-[1200px] px-3 py-4 sm:px-4 sm:py-6">
        <slot />
      </div>
    </main>
    <AppFooter />
  </div>
</template>
