<script setup lang="ts">
// The "Overview" page — the first item in the top nav (routed at /overview).
//
// A tabbed container holding two dashboards:
//   - Overview     → the new FreeAgent-style financial dashboard (OverviewPanel).
//                    Placeholder for now; the data cards land in follow-ups.
//   - VAT Dashboard → the existing HMRC MTD VAT dashboard (VatDashboardPanel).
//
// This view owns the single AppLayout wrapper + the underline tab bar (the same
// tab idiom as VatReturnDetailView); each panel renders WITHOUT its own AppLayout.
// We use v-if/v-else (not v-show) so the VAT panel only mounts — and only makes
// its live HMRC calls — when its tab is actually opened.
import { ref } from 'vue'
import AppLayout from '@/layouts/AppLayout.vue'
import OverviewPanel from '@/views/OverviewPanel.vue'
import VatDashboardPanel from '@/views/VatDashboardPanel.vue'

const tab = ref<'overview' | 'vat'>('overview')
</script>

<template>
  <AppLayout>
    <!-- Header -->
    <div class="mb-[18px]">
      <h1 class="text-[22px] font-bold">Overview</h1>
    </div>

    <!-- Tabs -->
    <div class="mb-5 flex gap-6 border-b border-fa-border">
      <button
        type="button"
        class="-mb-px border-b-2 px-1 py-2 text-sm font-semibold"
        :class="tab === 'overview' ? 'border-fa-blue text-fa-text' : 'border-transparent text-fa-muted hover:text-fa-text'"
        @click="tab = 'overview'"
      >
        Overview
      </button>
      <button
        type="button"
        class="-mb-px border-b-2 px-1 py-2 text-sm font-semibold"
        :class="tab === 'vat' ? 'border-fa-blue text-fa-text' : 'border-transparent text-fa-muted hover:text-fa-text'"
        @click="tab = 'vat'"
      >
        VAT Dashboard
      </button>
    </div>

    <OverviewPanel v-if="tab === 'overview'" />
    <VatDashboardPanel v-else />
  </AppLayout>
</template>
