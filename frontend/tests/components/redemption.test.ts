import { beforeEach, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import PointRedeem from '@/components/PointRedeem.vue'
import { useAuthStore } from '@/stores/auth'
import { apiPost } from '@/api'
vi.mock('@/api', () => ({ apiPost: vi.fn(), apiGet: vi.fn() }))
beforeEach(() => { vi.resetAllMocks(); setActivePinia(createPinia()) })
function render() {
 const auth = useAuthStore()
 auth.user = { id: 1, username: 'user', email: '', email_verified: true, role: 'user', is_admin: false, status: 'active', points: 30 }
 vi.spyOn(auth, 'fetchMe').mockResolvedValue(undefined)
 return { auth, wrapper: mount(PointRedeem) }
}
it('duplicate Enter/click submits once, credits balance and clears the used code', async () => {
 let resolve!: (value: { points: number; balance: number }) => void
 vi.mocked(apiPost).mockReturnValue(new Promise(r => { resolve = r }))
 const { auth, wrapper } = render()
 await wrapper.get('input').setValue('QZ-test-code')
 await wrapper.get('form').trigger('submit'); await wrapper.get('form').trigger('submit')
 expect(apiPost).toHaveBeenCalledTimes(1)
 resolve({ points: 50, balance: 80 }); await flushPromises()
 expect(auth.user?.points).toBe(80)
 expect((wrapper.get('input').element as HTMLInputElement).value).toBe('')
 expect(wrapper.get('[role="status"]').text()).toContain('已到账 50')
 expect(wrapper.emitted('redeemed')).toHaveLength(1)
 wrapper.unmount()
})
it('failure is announced and refreshes balance without automatically repeating the write', async () => {
 vi.mocked(apiPost).mockRejectedValue(new Error('兑换码已过期'))
 const { auth, wrapper } = render()
 await wrapper.get('input').setValue('QZ-expired'); await wrapper.get('form').trigger('submit'); await flushPromises()
 expect(apiPost).toHaveBeenCalledTimes(1)
 expect(auth.fetchMe).toHaveBeenCalledTimes(1)
 expect(wrapper.get('[role="alert"]').text()).toBe('兑换码已过期')
 expect(wrapper.emitted('redeemed')).toBeUndefined()
 expect(auth.user?.points).toBe(30)
 wrapper.unmount()
})
