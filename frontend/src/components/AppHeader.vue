<template>
  <header class="app-header">
    <div class="header-left" @click="router.push('/')">
      <div class="logo"><BrandMark :size="38" /></div>
      <span class="brand-name">{{ config.config.site_name || '轻舟' }}</span>
    </div>
    <div class="header-right">
      <template v-if="auth.isLoggedIn">
        <n-button quaternary size="small" @click="router.push('/dashboard')">
          <template #icon><n-icon><HomeOutline /></n-icon></template>
          控制台
        </n-button>
        <n-dropdown v-if="auth.isAdmin" :options="adminMenu" @select="handleNav">
          <n-button quaternary size="small">
            <template #icon><n-icon><SettingsOutline /></n-icon></template>
            管理
          </n-button>
        </n-dropdown>
        <n-dropdown :options="userMenu" @select="handleNav">
          <n-button quaternary size="small">
            <template #icon><n-icon><PersonOutline /></n-icon></template>
            {{ auth.user?.username }}
          </n-button>
        </n-dropdown>
      </template>
      <template v-else>
        <n-button type="primary" size="small" @click="showLogin = true">
          登录
        </n-button>
      </template>
    </div>

    <LoginDialog v-model:show="showLogin" />
  </header>
</template>

<script setup lang="ts">
import { ref, h, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NDropdown, NIcon } from 'naive-ui'
import { HomeOutline, SettingsOutline, PersonOutline, LogOutOutline } from '@vicons/ionicons5'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import LoginDialog from './LoginDialog.vue'
import BrandMark from './BrandMark.vue'

const router = useRouter()
const auth = useAuthStore()
const config = useConfigStore()
const showLogin = ref(router.currentRoute.value.query.login === '1')

// Store the unregister fn and drop the hook on unmount so it doesn't accumulate
// across remounts (each stale hook could re-open the login dialog).
const stopAfterEach = router.afterEach((to) => {
  if (to.query.login === '1') showLogin.value = true
})
onUnmounted(stopAfterEach)

const userMenu = [
  { label: '控制台', key: '/dashboard' },
  { label: '账户设置', key: '/account' },
  { type: 'divider', key: 'd1' },
  { label: '退出登录', key: '__logout', icon: () => h(NIcon, null, { default: () => h(LogOutOutline) }) },
]

const adminMenu = [
  { label: '管理概览', key: '/admin' },
  { label: '用户管理', key: '/admin/users' },
  { label: '套餐管理', key: '/admin/packages' },
  { label: '节点管理', key: '/admin/nodes' },
  { label: 'sing-box', key: '/admin/singbox' },
  { label: '服务器', key: '/admin/servers' },
  { label: '监控', key: '/admin/monitor' },
  { label: '订单', key: '/admin/orders' },
  { label: '系统设置', key: '/admin/settings' },
]

function handleNav(key: string) {
  if (key === '__logout') {
    auth.logout()
    router.push('/')
  } else {
    router.push(key)
  }
}
</script>

<style scoped>
.app-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 64px;
  padding: 0 clamp(16px, 4vw, 48px);
  background: rgba(245, 247, 249, .72);
  border-bottom: 1px solid rgba(28,48,70,.06);
  backdrop-filter: blur(22px) saturate(1.18);
  position: sticky;
  top: 0;
  z-index: 100;
}
.header-left { display: flex; align-items: center; gap: 10px; cursor: pointer; user-select: none; }
.logo {
  width: 38px; height: 38px; display: grid; place-items: center;
}
.brand-name { font-weight: 650; font-size: 17px; letter-spacing: -0.02em; color: var(--text); }
.header-right { display: flex; align-items: center; gap: 8px; }

@media (max-width: 640px) {
  .app-header { padding: 0 12px; }
  .header-right { gap: 2px; }
}
</style>
