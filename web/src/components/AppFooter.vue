<script setup lang="ts">
// Pale-blue footer, mirroring FA's. Static placeholder links.
const links = ['Support', 'Knowledge Base', 'Privacy', 'Cookie Notice']
</script>

<template>
  <footer class="mt-10 border-t border-fa-border bg-fa-footer">
    <div class="mx-auto flex max-w-[1200px] flex-wrap items-center justify-between gap-2 p-4">
      <span class="font-bold text-fa-blue">Accounting</span>
      <nav class="flex flex-wrap gap-[18px]">
        <a
          v-for="link in links"
          :key="link"
          href="#"
          class="text-[13px] text-fa-blue hover:underline"
          >{{ link }}</a
        >
      </nav>
    </div>
  </footer>
</template>
