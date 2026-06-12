<script setup lang="ts">
// App shell used by the authenticated pages (list / entry / detail):
//   top navigation  →  page content (<slot/>)  →  footer
// LoginView does NOT use this — it is a standalone full-page screen.
import AppTopBar from '@/components/AppTopBar.vue'
import AppFooter from '@/components/AppFooter.vue'
</script>

<template>
  <div class="layout">
    <AppTopBar />
    <main class="layout__main">
      <div class="layout__container">
        <slot />
      </div>
    </main>
    <AppFooter />
  </div>
</template>

<style scoped>
.layout {
  min-height: 100%;
  display: flex;
  flex-direction: column;
  background: #ffffff;
}
.layout__main {
  flex: 1 0 auto;
}
.layout__container {
  max-width: 1200px;
  margin: 0 auto;
  padding: 24px 16px;
}
</style>
