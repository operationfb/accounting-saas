<script setup lang="ts">
// Pale-blue footer, mirroring FreeAgent's. Static placeholder links.
const links = ['Support', 'Knowledge Base', 'Privacy', 'Cookie Notice']
</script>

<template>
  <footer class="footer">
    <div class="footer__inner">
      <span class="footer__brand">Accounting</span>
      <nav class="footer__links">
        <a v-for="link in links" :key="link" href="#" class="fa-link footer__link">{{ link }}</a>
      </nav>
    </div>
  </footer>
</template>

<style scoped>
.footer {
  background: var(--fa-footer);
  border-top: 1px solid var(--fa-border);
  margin-top: 40px;
}
.footer__inner {
  max-width: 1200px;
  margin: 0 auto;
  padding: 16px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
}
.footer__brand {
  font-weight: 700;
  color: var(--fa-blue);
}
.footer__links {
  display: flex;
  gap: 18px;
  flex-wrap: wrap;
}
.footer__link {
  font-size: 13px;
}
</style>
