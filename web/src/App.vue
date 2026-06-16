<script setup lang="ts">
// Root component — just renders the active route.
// Each view decides whether to wrap itself in AppLayout (the app chrome) or
// stand alone (LoginView).
</script>

<template>
  <!-- Key by path so routes that share a component remount cleanly. This matters
       for Smart Upload: /expenses/new and /expenses/:id/edit are the SAME component
       (ExpenseEntryView), and without a key vue-router would reuse the instance on
       the create→edit redirect, skipping onMounted (so it'd never flip to edit mode
       or start OCR polling). Keying by path (not fullPath) ignores query-only changes. -->
  <RouterView :key="$route.path" />
</template>
