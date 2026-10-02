import { beforeEach, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AdminAudience from '@/components/AdminAudience.vue'
import AdminMachineUsage from '@/components/AdminMachineUsage.vue'
import { apiGet, apiList } from '@/api'
vi.mock('@/api', () => ({ apiGet: vi.fn(), apiList: vi.fn() }))
beforeEach(() => vi.resetAllMocks())
const member = (name:string) => ({ users: [{ user_id: 1, username:name, buckets:[{ id:1, name:'有效套餐', kind:'plan', used_up:0, used_down:0, traffic_limit:1024, expiry_at:0 }] }], total:1 })
it('includes zero-use users and labels allowance scope rather than online/node usage', async () => {
 vi.mocked(apiGet).mockResolvedValue(member('zero-user'))
 const w = mount(AdminAudience, { props:{scope:'node', id:1} }); await flushPromises()
 expect(w.text()).toContain('zero-user'); expect(w.text()).toContain('有效套餐'); expect(w.text()).toContain('不是在线人数'); expect(w.text()).toContain('不代表此节点或机器上的用量')
 expect(w.findAll('tbody tr')).toHaveLength(1); w.unmount()
})
it('ignores a stale response after selecting a different node', async () => {
 let resolveOld!:(value:any)=>void
 vi.mocked(apiGet).mockImplementationOnce(() => new Promise(resolve => {resolveOld=resolve})).mockResolvedValueOnce(member('new-user'))
 const w = mount(AdminAudience, { props:{scope:'node',id:1} }); await w.setProps({id:2}); await flushPromises()
 resolveOld(member('stale-user')); await flushPromises()
 expect(w.text()).toContain('new-user'); expect(w.text()).not.toContain('stale-user'); w.unmount()
})
it('does not present a failed query as zero eligible users and supports retry', async () => {
 vi.mocked(apiGet).mockRejectedValueOnce(new Error('read failed')).mockResolvedValueOnce(member('retry-user'))
 const w = mount(AdminAudience,{props:{scope:'group',id:1}}); await flushPromises()
 expect(w.text()).toContain('read failed'); expect(w.text()).not.toContain('暂无有权限的用户')
 await w.findAll('button').find(b=>b.text()==='重试')!.trigger('click'); await flushPromises();expect(w.text()).toContain('retry-user'); w.unmount()
})
it('machine report keeps absent history explicit and documents missing package attribution', async () => {
 vi.mocked(apiList).mockResolvedValue([{id:1,name:'machine'}]);vi.mocked(apiGet).mockResolvedValue({report:{coverage_start:0,coverage_end:0,up:0,down:0,user_count:0,users:[],days:[]}})
 const w = mount(AdminMachineUsage); await flushPromises()
 const selects=w.findAllComponents({name:'Select'});expect(selects.length).toBeGreaterThan(0)
 selects[0]!.vm.$emit('update:value',1); await flushPromises()
 expect(w.text()).toContain('不能据此判断没有使用');expect(w.text()).toContain('无法追溯此机器的套餐归属');expect(w.text()).toContain('不是网卡')
 w.unmount()
})
it('machine selection and time changes cannot resurrect stale rankings', async () => {
 let resolveOld!:(value:any)=>void
 vi.mocked(apiList).mockResolvedValue([{id:1,name:'first'},{id:2,name:'second'}])
 const report = (name:string) => ({report:{coverage_start:1,coverage_end:2,up:10,down:20,user_count:1,users:[{user_id:1,username:name,up:10,down:20,total:30}],days:[]}})
 vi.mocked(apiGet).mockImplementationOnce(()=>new Promise(resolve=>{resolveOld=resolve})).mockResolvedValueOnce(report('current-rank'))
 const w=mount(AdminMachineUsage);await flushPromises();const select=w.findAllComponents({name:'Select'})[0]!
 select.vm.$emit('update:value',1);await flushPromises();select.vm.$emit('update:value',2);await flushPromises()
 resolveOld(report('stale-rank'));await flushPromises();expect(w.text()).toContain('current-rank');expect(w.text()).not.toContain('stale-rank')
 expect(vi.mocked(apiGet).mock.calls[1]![0]).toContain('server=2');w.unmount()
})
it('machine picker failure stays visible without a selected machine',async()=>{
 vi.mocked(apiList).mockRejectedValue(new Error('machine list unavailable'))
 const w=mount(AdminMachineUsage);await flushPromises();expect(w.text()).toContain('machine list unavailable');w.unmount()
})
it('panel-local machine zero is selectable and loads traffic and eligible users',async()=>{
 vi.mocked(apiList).mockResolvedValue([])
 vi.mocked(apiGet).mockImplementation(async(path)=>path.includes('/audience?')?member('local-member'):{report:{coverage_start:1,coverage_end:2,up:10,down:20,user_count:0,users:[],days:[]}})
 const w=mount(AdminMachineUsage);await flushPromises();const select=w.findAllComponents({name:'Select'})[0]!
 expect(select.props('options')).toContainEqual({label:'面板本机',value:0});select.vm.$emit('update:value',0);await flushPromises()
 expect(vi.mocked(apiGet).mock.calls.some(c=>c[0].includes('server=0'))).toBe(true)
 expect(w.findAll('button').find(b=>b.text()==='刷新')!.attributes('disabled')).toBeUndefined()
 const tabs=w.findComponent({name:'Tabs'});tabs.vm.$emit('update:value','eligible');await flushPromises()
 expect(vi.mocked(apiGet).mock.calls.some(c=>c[0].includes('scope=server&id=0'))).toBe(true);expect(w.text()).toContain('local-member');w.unmount()
})
