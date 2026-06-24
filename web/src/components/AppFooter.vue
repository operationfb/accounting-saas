<script setup lang="ts">
// Pale-blue footer, mirroring FA's. Static placeholder links.
// Same imported asset as the login page (Vite fingerprints it for cache-busting).
import kontalaLogo from '@/assets/kontala-logo-green.png'

const links = ['Support', 'Knowledge Base', 'Privacy', 'Cookie Notice']
</script>

<template>
  <footer class="mt-10 border-t border-fa-border bg-fa-footer">
    <div class="mx-auto flex max-w-[1200px] flex-wrap items-center justify-between gap-2 p-4">
      <img :src="kontalaLogo" alt="Kontala" class="h-6 w-auto select-none" />
      <nav class="flex flex-wrap gap-[18px]">
        <a
          v-for="link in links"
          :key="link"
          href="#"
          class="text-[13px] text-fa-green hover:underline"
          >{{ link }}</a
        >
      </nav>
    </div>
  </footer>
</template>
