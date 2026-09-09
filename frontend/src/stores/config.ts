import { defineStore } from 'pinia'
import { ref } from 'vue'
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
  help_docs_mode: string
  help_docs_url: string
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
    help_docs_mode: 'builtin',
    help_docs_url: '',
  })

  async function fetchConfig() {
    try {
      const data = await apiGet<SiteConfig>('/api/config')
      if (data) Object.assign(config.value, data)
    } catch {}
    return config.value
  }

  return { config, fetchConfig }
})
