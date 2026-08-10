<script setup lang="ts">
import { toastState } from '@/utils/clipboard'
</script>

<template>
  <Transition name="toast">
    <div v-if="toastState.visible" class="toast" :class="toastState.kind === 'bad' ? 'is-bad' : 'is-ok'">
      <svg v-if="toastState.kind === 'ok'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="m20 6-11 11-5-5"/></svg>
      <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18M6 6l12 12"/></svg>
      <span>{{ toastState.message }}</span>
    </div>
  </Transition>
</template>
