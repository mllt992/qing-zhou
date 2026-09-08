<template>
  <div class="app-shell" :class="{ mobile: isMobile }">
    <!-- 桌面侧边栏 -->
    <aside v-if="!isMobile" class="app-sider">
      <div class="sidebar-brand" @click="router.push('/')">
        <div class="sidebar-logo"><BrandMark :size="40" /></div>
        <div class="brand-copy">
          <span class="brand-text">{{ config.config.site_name || '轻舟' }}</span>
          <span class="brand-caption">服务控制台</span>
        </div>
      </div>
      <nav class="sidebar-menu">
        <n-menu :value="activeKey" :options="menuOptions" :default-expanded-keys="['admin-root']" :indent="18" @update:value="handleMenuSelect" />
      </nav>
    </aside>

    <!-- 移动端抽屉 -->
    <n-drawer v-model:show="drawerShow" placement="left" :width="260" :block-scroll="true">
      <n-drawer-content :native-scrollbar="true" body-content-style="padding:0;">
        <div class="sidebar-brand" @click="goAndClose('/')">
          <div class="sidebar-logo"><BrandMark :size="40" /></div>
          <div class="brand-copy">
            <span class="brand-text">{{ config.config.site_name || '轻舟' }}</span>
            <span class="brand-caption">服务控制台</span>
          </div>
        </div>
        <nav class="sidebar-menu">
          <n-menu :value="activeKey" :options="menuOptions" :default-expanded-keys="['admin-root']" :indent="18" @update:value="goAndClose" />
        </nav>
      </n-drawer-content>
    </n-drawer>

    <!-- 主区 -->
    <div class="app-main">
      <header class="layout-header">
        <div class="header-left">
          <button v-if="isMobile" class="icon-btn" @click="drawerShow = true" aria-label="菜单">
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="21" y2="18"/></svg>
          </button>
          <span class="header-title">{{ currentTitle }}</span>
        </div>
        <div v-if="!isMobile" class="header-search">
          <n-icon class="header-search-icon" :size="17"><SearchOutline /></n-icon>
          <n-auto-complete
            v-model:value="searchQuery"
            :options="searchOptions"
            placeholder="搜索功能"
            clear-after-select
            @select="handleSearchSelect"
            @keydown.enter="openFirstSearchResult"
          />
          <kbd>Ctrl K</kbd>
        </div>
        <div class="header-right">
          <template v-if="auth.isAdmin && !isMobile">
            <n-dropdown :options="adminQuickMenu" @select="handleAdminSelect">
              <n-button quaternary size="small">管理</n-button>
            </n-dropdown>
          </template>
          <n-dropdown :options="userMenu" @select="handleUserSelect">
            <n-button quaternary size="small" class="account-button">
              <span class="user-avatar" aria-hidden="true">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="3.25"/><path d="M5.9 19c.75-3.35 2.8-5.1 6.1-5.1s5.35 1.75 6.1 5.1"/></svg>
              </span>
              <span v-if="!isMobile" class="account-name">{{ auth.user?.username }}</span>
            </n-button>
          </n-dropdown>
        </div>
      </header>
      <main class="layout-content">
        <router-view v-slot="{ Component, route: viewRoute }">
          <div :key="viewRoute.path" class="route-page-shell">
            <component :is="Component" :key="viewRoute.path" />
          </div>
        </router-view>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { h, computed, ref, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { NDrawer, NDrawerContent, NButton, NDropdown, NIcon, NMenu, NAutoComplete } from 'naive-ui'
import type { MenuOption } from 'naive-ui'
import {
  SpeedometerOutline, LinkOutline, CartOutline,
  ReceiptOutline, WalletOutline, MegaphoneOutline, BookOutline,
  PersonOutline, PeopleOutline, PeopleCircleOutline, ArchiveOutline, ServerOutline,
  SettingsOutline, KeyOutline, NotificationsOutline, DocumentTextOutline,
  PulseOutline, HardwareChipOutline, HomeOutline, LogOutOutline, CloudDownloadOutline,
  ShieldCheckmarkOutline, SearchOutline
} from '@vicons/ionicons5'
import { useAuthStore } from '@/stores/auth'
import { useConfigStore } from '@/stores/config'
import BrandMark from './BrandMark.vue'
import { openHelp } from '@/utils/help'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const config = useConfigStore()

const activeKey = computed(() => route.path)
const searchQuery = ref('')

// ---- 响应式：移动端判定 ----
const isMobile = ref(false)
const drawerShow = ref(false)

function checkMobile() {
  isMobile.value = window.matchMedia('(max-width: 768px)').matches
  if (!isMobile.value) drawerShow.value = false
}
onMounted(() => { checkMobile(); window.addEventListener('resize', checkMobile) })
onUnmounted(() => window.removeEventListener('resize', checkMobile))

function renderIcon(icon: any) {
  return () => h(NIcon, null, { default: () => h(icon) })
}

function groupLabel(text: string) {
  return () => h('span', { class: 'menu-group-label' }, text)
}

const userMenuItems: MenuOption[] = [
  { label: '控制台', key: '/dashboard', icon: renderIcon(SpeedometerOutline) },
  { label: '订阅管理', key: '/sub', icon: renderIcon(LinkOutline) },
]

const shopItems: MenuOption[] = [
  { label: '积分商城', key: '/shop', icon: renderIcon(CartOutline) },
  { label: '订单记录', key: '/orders', icon: renderIcon(ReceiptOutline) },
  { label: '积分明细', key: '/points', icon: renderIcon(WalletOutline) },
]

const infoItems: MenuOption[] = [
  { label: '公告通知', key: '/notices', icon: renderIcon(MegaphoneOutline) },
  { label: '帮助中心', key: '/help', icon: renderIcon(BookOutline) },
]

const adminOpsItems: MenuOption[] = [
  { label: '管理概览', key: '/admin', icon: renderIcon(SpeedometerOutline) },
  { label: '用户管理', key: '/admin/users', icon: renderIcon(PeopleOutline) },
  { label: '用户组', key: '/admin/user-groups', icon: renderIcon(PeopleCircleOutline) },
  { label: '套餐管理', key: '/admin/packages', icon: renderIcon(ArchiveOutline) },
  { label: '订单管理', key: '/admin/orders', icon: renderIcon(ReceiptOutline) },
  { label: '注册码', key: '/admin/reg-codes', icon: renderIcon(KeyOutline) },
  { label: 'API Token', key: '/admin/api-tokens', icon: renderIcon(KeyOutline) },
]
const adminNodeItems: MenuOption[] = [
  { label: '节点管理', key: '/admin/nodes', icon: renderIcon(ServerOutline) },
  { label: 'sing-box', key: '/admin/singbox', icon: renderIcon(HardwareChipOutline) },
  { label: '证书管理', key: '/admin/certs', icon: renderIcon(ShieldCheckmarkOutline) },
  { label: '服务器', key: '/admin/servers', icon: renderIcon(ServerOutline) },
  { label: '监控管理', key: '/admin/monitor', icon: renderIcon(PulseOutline) },
]
const adminSysItems: MenuOption[] = [
  { label: '公告管理', key: '/admin/announcements', icon: renderIcon(NotificationsOutline) },
  { label: '手动通知', key: '/admin/manual-notifications', icon: renderIcon(MegaphoneOutline) },
  { label: '帮助文档', key: '/admin/help', icon: renderIcon(DocumentTextOutline) },
  { label: '系统设置', key: '/admin/settings', icon: renderIcon(SettingsOutline) },
  { label: '在线更新', key: '/admin/update', icon: renderIcon(CloudDownloadOutline) },
]

const menuOptions = computed<MenuOption[]>(() => {
  const items: MenuOption[] = [
    { label: '首页', key: '/', icon: renderIcon(HomeOutline) },
    { type: 'group', key: 'g-common', label: groupLabel('常用'), children: userMenuItems },
    { type: 'group', key: 'g-shop', label: groupLabel('商城'), children: shopItems },
    { type: 'group', key: 'g-info', label: groupLabel('信息'), children: infoItems },
    { label: '账户设置', key: '/account', icon: renderIcon(PersonOutline) },
  ]
  if (auth.isAdmin) {
    items.push({
      label: '管理后台', key: 'admin-root', icon: renderIcon(SettingsOutline),
      children: [
        { type: 'group', key: 'ag-ops', label: groupLabel('运营'), children: adminOpsItems },
        { type: 'group', key: 'ag-node', label: groupLabel('节点服务'), children: adminNodeItems },
        { type: 'group', key: 'ag-sys', label: groupLabel('内容系统'), children: adminSysItems },
      ],
    })
  }
  return items
})

const titleMap: Record<string, string> = {
  '/': '首页', '/dashboard': '控制台', '/sub': '订阅管理', '/shop': '积分商城',
  '/orders': '订单记录', '/points': '积分明细', '/notices': '公告通知', '/help': '帮助中心', '/account': '账户设置',
  '/admin': '管理概览', '/admin/users': '用户管理', '/admin/user-groups': '用户组', '/admin/packages': '套餐管理', '/admin/nodes': '节点管理',
  '/admin/singbox': 'sing-box', '/admin/certs': '证书管理', '/admin/orders': '订单管理', '/admin/servers': '服务器', '/admin/monitor': '监控管理',
  '/admin/settings': '系统设置', '/admin/reg-codes': '注册码', '/admin/api-tokens': 'API Token', '/admin/announcements': '公告管理', '/admin/manual-notifications': '手动通知', '/admin/help': '帮助文档',
  '/admin/update': '在线更新',
}
const currentTitle = computed(() => titleMap[route.path] || config.config.site_name || '轻舟')

const searchItems = computed(() => {
  const items = [
    { label: '首页', value: '/' },
    ...userMenuItems.map(item => ({ label: String(item.label), value: String(item.key) })),
    ...shopItems.map(item => ({ label: String(item.label), value: String(item.key) })),
    ...infoItems.map(item => ({ label: String(item.label), value: String(item.key) })),
    { label: '账户设置', value: '/account' },
  ]
  if (auth.isAdmin) {
    items.push(
      ...adminOpsItems.map(item => ({ label: String(item.label), value: String(item.key) })),
      ...adminNodeItems.map(item => ({ label: String(item.label), value: String(item.key) })),
      ...adminSysItems.map(item => ({ label: String(item.label), value: String(item.key) })),
    )
  }
  return items
})

const searchOptions = computed(() => {
  const query = (searchQuery.value || '').trim().toLowerCase()
  if (!query) return []
  return searchItems.value
    .filter(item => item.label.toLowerCase().includes(query) || item.value.toLowerCase().includes(query))
    .slice(0, 8)
})

const userMenu = [
  { label: '退出登录', key: 'logout', icon: () => h(NIcon, null, { default: () => h(LogOutOutline) }) },
]
const adminQuickMenu = [
  { label: '管理概览', key: '/admin' },
  { label: '用户管理', key: '/admin/users' },
  { label: '系统设置', key: '/admin/settings' },
]

function handleMenuSelect(key: string) {
  if (key === 'admin-root') return
  if (key === '/help') openHelp(config.config, router)
  else router.push(key)
}
function goAndClose(key: string) {
  if (key === 'admin-root') return
  drawerShow.value = false
  if (key === '/help') openHelp(config.config, router)
  else router.push(key)
}
function handleUserSelect(key: string) {
  if (key === 'logout') { auth.logout(); router.push('/') }
}
function handleAdminSelect(key: string) { router.push(key) }
function handleSearchSelect(path: string) {
  searchQuery.value = ''
  if (path === '/help') openHelp(config.config, router)
  else router.push(path)
}
function openFirstSearchResult() {
  const first = searchOptions.value[0]
  if (first) handleSearchSelect(first.value)
}

function focusSearch(event: KeyboardEvent) {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    document.querySelector<HTMLInputElement>('.header-search input')?.focus()
  }
}

onMounted(() => window.addEventListener('keydown', focusSearch))
onUnmounted(() => window.removeEventListener('keydown', focusSearch))
</script>

<style scoped>
.app-shell { display: flex; min-height: 100vh; background: var(--bg); }
.app-sider {
  width: 236px; flex-shrink: 0;
  background: var(--bg);
  position: sticky; top: 0; height: 100vh;
  display: flex; flex-direction: column;
}
.sidebar-brand {
  display: flex; align-items: center; gap: 10px;
  min-height: 64px; padding: 10px 16px;
  font-weight: 750; font-size: 17px; cursor: pointer;
  letter-spacing: -0.02em;
}
.sidebar-logo {
  width: 40px; height: 40px; display: grid; place-items: center;
  flex-shrink: 0;
}
.brand-copy { min-width: 0; display: flex; flex-direction: column; line-height: 1.2; }
.brand-text { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.brand-caption { margin-top: 3px; color: var(--text-3); font-size: 10.5px; font-weight: 500; letter-spacing: .02em; }
.sidebar-menu { flex: 1; overflow-y: auto; padding: 4px 8px 16px; }

.app-main { flex: 1; min-width: 0; display: flex; flex-direction: column; min-height: 100vh; background: var(--bg); }
.layout-header {
  height: 64px; display: grid; grid-template-columns: minmax(140px, 1fr) minmax(280px, 480px) minmax(140px, 1fr);
  align-items: center; gap: 24px; padding: 0 24px;
  background: rgba(245, 247, 249, .74);
  border-bottom: 1px solid rgba(28,48,70,.055);
  backdrop-filter: blur(22px) saturate(1.18);
  position: sticky; top: 0; z-index: 10;
}
.header-left { display: flex; align-items: center; gap: 10px; min-width: 0; }
.header-title { font-weight: 650; font-size: 16px; color: var(--text); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.header-search { position: relative; width: 100%; }
.header-search-icon { position: absolute; left: 13px; top: 50%; z-index: 2; transform: translateY(-50%); color: var(--text-2); pointer-events: none; }
.header-search :deep(.n-input) { height: 36px; border-radius: 999px !important; box-shadow: var(--shadow-sm); transition: box-shadow .3s var(--ease-standard), background .3s var(--ease-standard) !important; }
.header-search :deep(.n-input.n-input--focus) { background: rgba(255,255,255,.97) !important; box-shadow: 0 8px 24px rgba(34,75,108,.1), 0 0 0 1px rgba(23,105,165,.12); }
.header-search :deep(.n-input-wrapper) { padding-left: 39px !important; padding-right: 68px !important; }
.header-search :deep(.n-input__input-el) { padding: 0 !important; }
.header-search kbd {
  position: absolute; right: 10px; top: 50%; transform: translateY(-50%); pointer-events: none;
  padding: 1px 6px; border: 1px solid var(--border); border-bottom-color: var(--border-strong);
  border-radius: 5px; background: var(--bg-subtle); color: var(--text-3); font: 10px/16px var(--ff);
}
.header-right { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex-shrink: 0; }
.account-button {
  min-height: 34px; padding: 3px 9px 3px 4px !important; border-radius: 999px !important;
  color: var(--text-2) !important; transition: background .26s var(--ease-standard), color .26s var(--ease-standard) !important;
}
.account-button :deep(.n-button__content) { gap: 7px; }
.account-button:hover { background: rgba(255,255,255,.72) !important; color: var(--text) !important; }
.user-avatar {
  width: 27px; height: 27px; flex: 0 0 27px; box-sizing: border-box; display: inline-grid; place-items: center; border-radius: 50%;
  background: linear-gradient(180deg, rgba(255,255,255,.96), rgba(242,246,249,.92));
  color: #496274; border: 1px solid var(--border-strong);
  box-shadow: inset 0 1px 0 rgba(255,255,255,.95), 0 2px 5px rgba(38,58,76,.08);
}
.user-avatar svg { width: 16px; height: 16px; display: block; }
.account-name { max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.app-shell.mobile .account-button { width: 34px; padding: 3px !important; }
.icon-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 36px; height: 36px; border-radius: 9px; border: 1px solid var(--border);
  background: #fff; color: var(--text-2); cursor: pointer; padding: 0;
}
.icon-btn:hover { background: var(--bg-soft); }

.layout-content { flex: 1; padding: 22px 28px 40px; max-width: 1360px; margin: 0 auto; width: 100%; box-sizing: border-box; }
.route-page-shell { animation: route-page-in .2s var(--ease-emphasized) both; }
@keyframes route-page-in { from { opacity:.35; transform:translateY(3px); } to { opacity:1; transform:none; } }

/* 移动端 */
.app-shell.mobile .layout-header { display: flex; justify-content: space-between; padding: 0 12px; }
.app-shell.mobile .layout-content { padding: 14px 12px; }

/* 分组标签样式 */
:deep(.menu-group-label) {
  font-size: 11px; font-weight: 650; color: var(--text-2);
  letter-spacing: 0;
}
:deep(.n-menu-item-group-title) { padding: 14px 10px 5px !important; }
:deep(.n-menu-item-content) { border-radius: 8px; margin: 1px 0; }
:deep(.n-menu-item-content::before) { border-radius: 8px !important; }
:deep(.n-menu-item-content--selected::before) { background: rgba(255,255,255,.9) !important; box-shadow: var(--shadow-sm); }
:deep(.n-menu-item-content--selected::after) {
  content: ''; position: absolute; left: 1px; top: 10px; bottom: 10px; width: 3px;
  border-radius: 3px; background: var(--accent);
}
:deep(.n-menu-item-content--selected .n-menu-item-content-header) { font-weight: 650; color: var(--text) !important; }

@media (max-width: 1080px) {
  .layout-header { grid-template-columns: minmax(120px, .8fr) minmax(220px, 380px) minmax(120px, .8fr); gap: 14px; }
}
</style>
