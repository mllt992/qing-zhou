import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { apiGet } from '@/api'

export interface SiteConfig {
  oauth2_enabled: boolean
  oauth2_name: string
  site_name: string
  site_description: string
  register_mode: string
  registration_open: boolean
  email_verify_required: boolean
  // 面板到底能不能发信。发不了的话，「找回密码」是条死路——链接只会写进
  // 服务端日志，用户永远等不到那封邮件。
  email_enabled: boolean
  telegram_enabled: boolean
  points_per_cny: number
  homepage_mode: string
  homepage_url: string
  homepage_machine_health: boolean
  help_docs_mode: string
  help_docs_url: string
  brand_icon_data_uri: string
}

export const useConfigStore = defineStore('config', () => {
  const config = ref<SiteConfig>({
    oauth2_enabled: false,
    oauth2_name: '认证中心',
    site_name: '轻舟',
    site_description: '',
    register_mode: 'open',
    registration_open: true,
    email_verify_required: true,
    // 默认 true：拿不到 /api/config 时维持原样（显示找回密码入口），
    // 而不是因为一次网络抖动就把功能藏起来。
    email_enabled: true,
    telegram_enabled: false,
    points_per_cny: 10,
    homepage_mode: 'monitor',
    homepage_url: '',
    homepage_machine_health: false,
    help_docs_mode: 'builtin',
    help_docs_url: '',
    brand_icon_data_uri: '',
  })

  function applyBrowserBranding() {
    if (typeof document === 'undefined') return
    const name = config.value.site_name?.trim() || '轻舟'
    const icon = config.value.brand_icon_data_uri || '/qingzhou-mark.svg'
    const type = icon.startsWith('data:image/png') ? 'image/png'
      : icon.startsWith('data:image/jpeg') ? 'image/jpeg'
        : icon.startsWith('data:image/webp') ? 'image/webp' : 'image/svg+xml'
    document.title = name
    for (const rel of ['icon', 'shortcut icon', 'apple-touch-icon']) {
      let link = document.querySelector<HTMLLinkElement>(`link[rel="${rel}"]`)
      if (!link) {
        link = document.createElement('link')
        link.rel = rel
        document.head.appendChild(link)
      }
      link.href = icon
      link.type = type
    }
  }

  watch(config, applyBrowserBranding, { deep: true, immediate: true })

  async function fetchConfig() {
    try {
      const data = await apiGet<SiteConfig>('/api/config')
      if (data) Object.assign(config.value, data)
    } catch {}
    return config.value
  }

  return { config, fetchConfig, applyBrowserBranding }
})
