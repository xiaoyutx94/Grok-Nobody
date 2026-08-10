<script setup lang="ts">
import { confirmState, settleConfirm } from '@/utils/confirm'
</script>

<template>
  <div v-if="confirmState.visible" class="overlay" @click.self="settleConfirm(false)">
    <div class="sheet" style="max-width:420px">
      <div class="kicker">确认操作</div>
      <p style="margin:10px 0 18px;font-size:13.5px;line-height:1.6;white-space:pre-wrap">{{ confirmState.message }}</p>
      <div class="row" style="justify-content:flex-end;gap:10px">
        <button class="btn btn-secondary" @click="settleConfirm(false)">取消</button>
        <button class="btn btn-danger" @click="settleConfirm(true)">确定</button>
      </div>
    </div>
  </div>
</template>
