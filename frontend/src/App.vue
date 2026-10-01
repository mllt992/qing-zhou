<template>
  <n-config-provider :theme-overrides="themeOverrides" :locale="zhCN" :date-locale="dateZhCN">
    <n-message-provider>
      <n-dialog-provider>
        <router-view />
        <StepUpDialog />
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { NConfigProvider, NMessageProvider, NDialogProvider, zhCN, dateZhCN } from 'naive-ui'
import type { GlobalThemeOverrides } from 'naive-ui'
import { useConfigStore } from '@/stores/config'
import StepUpDialog from '@/components/StepUpDialog.vue'
import { watch } from 'vue'
import { useRoute } from 'vue-router'
import { cancelStepUpRequests } from '@/api/reauth'

const route = useRoute()
watch(() => route.fullPath, () => cancelStepUpRequests(), { flush: 'sync' })

const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#1769a5',
    primaryColorHover: '#125b91',
    primaryColorPressed: '#0d4c78',
    primaryColorSuppl: '#1769a5',
    infoColor: '#1769a5',
    successColor: '#337a5c',
    warningColor: '#b66b20',
    errorColor: '#b6413a',
    borderRadius: '10px',
    borderColor: 'rgba(28, 48, 70, .1)',
    textColorBase: '#1d2733',
    fontFamily: '"Segoe UI Variable", "Segoe UI", "Microsoft YaHei", "PingFang SC", system-ui, sans-serif',
  },
}

// 只拉站点配置；auth 由 router guard 负责初始化
const config = useConfigStore()
config.fetchConfig()
</script>
