import { beforeEach, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import PointRedeem from '@/components/PointRedeem.vue'
import { useAuthStore } from '@/stores/auth'
import { apiGet, apiPost } from '@/api'
vi.mock('@/api', () => ({ apiPost: vi.fn(), apiGet: vi.fn() }))
beforeEach(() => { vi.resetAllMocks(); setActivePinia(createPinia()) })
function render() {
 const auth = useAuthStore()
 auth.user = { id: 1, username: 'user', email: '', email_verified: true, role: 'user', is_admin: false, status: 'active', points: 30 }
 vi.mocked(apiGet).mockImplementation(async () => ({ ...auth.user }))
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
 expect(apiGet).toHaveBeenCalledTimes(1)
 expect(wrapper.get('[role="alert"]').text()).toBe('兑换码已过期')
 expect(wrapper.emitted('redeemed')).toBeUndefined()
 expect(auth.user?.points).toBe(30)
 wrapper.unmount()
})

it('a late successful redemption cannot overwrite another account balance', async () => {
 let resolve!: (value: { points: number; balance: number }) => void
 vi.mocked(apiPost).mockReturnValue(new Promise(r => { resolve = r }))
 const { auth, wrapper } = render()
 await wrapper.get('input').setValue('QZ-old-account'); await wrapper.get('form').trigger('submit')
 auth.user = { ...auth.user!, id: 2, username: 'other', points: 500 }
 resolve({ points: 50, balance: 80 }); await flushPromises()
 expect(auth.user.points).toBe(500)
 expect(wrapper.emitted('redeemed')).toBeUndefined()
 expect(wrapper.find('[role="status"]').exists()).toBe(false)
 wrapper.unmount()
})
it('unmount ignores a late response and does not emit or mutate the account', async () => {
 let resolve!: (value: { points: number; balance: number }) => void
 vi.mocked(apiPost).mockReturnValue(new Promise(r => { resolve = r }))
 const { auth, wrapper } = render()
 await wrapper.get('input').setValue('QZ-leaving'); await wrapper.get('form').trigger('submit')
 wrapper.unmount(); resolve({ points: 50, balance: 80 }); await flushPromises()
 expect(auth.user?.points).toBe(30)
 expect(wrapper.emitted('redeemed')).toBeUndefined()
})
it('a late failure from an old account does not refresh the new account', async () => {
 let reject!: (error: Error) => void
 vi.mocked(apiPost).mockReturnValue(new Promise((_resolve, r) => { reject = r }))
 const { auth, wrapper } = render()
 await wrapper.get('input').setValue('QZ-old'); await wrapper.get('form').trigger('submit')
 auth.user = { ...auth.user!, id: 2, username: 'other', points: 500 }
 reject(new Error('old failure')); await flushPromises()
 expect(apiGet).not.toHaveBeenCalled()
 expect(wrapper.find('[role="alert"]').exists()).toBe(false)
 wrapper.unmount()
})
it('account changes during failure recovery also ignore the old profile response', async () => {
 vi.mocked(apiPost).mockRejectedValue(new Error('response lost'))
 const { auth, wrapper } = render()
 const original = { ...auth.user! }
 let resolve!: (value: typeof original) => void
 vi.mocked(apiGet).mockReturnValue(new Promise(r => { resolve = r }))
 await wrapper.get('input').setValue('QZ-recovery'); await wrapper.get('form').trigger('submit'); await flushPromises()
 auth.user = { ...original, id: 2, username: 'other', points: 500 }
 resolve({ ...original, points: 80 }); await flushPromises()
 expect(auth.user.id).toBe(2); expect(auth.user.points).toBe(500)
 wrapper.unmount()
})
