import { beforeEach, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AdminRelayMetering from '@/components/AdminRelayMetering.vue'
import { apiGet, apiPut } from '@/api'
vi.mock('@/api',()=>({apiGet:vi.fn(),apiPut:vi.fn(),apiPost:vi.fn()}))
beforeEach(()=>{vi.resetAllMocks();vi.mocked(apiGet).mockResolvedValue({enabled:false,cumulative_enabled:false,cumulative_started:false,links:[]});vi.mocked(apiPut).mockResolvedValue({started:true})})
it('requires reviewing credential and restart effects before applying',async()=>{
 const w=mount(AdminRelayMetering);await flushPromises();await w.find('form').trigger('submit');expect(apiPut).not.toHaveBeenCalled();expect(w.text()).toContain('生成并加密保存内部中转凭据');expect(w.text()).toContain('可能中断现有连接')
 await w.findAll('button').find(b=>b.text()==='确认变更')!.trigger('click');await flushPromises();expect(apiPut).toHaveBeenCalledWith('/api/admin/relay-metering',{enabled:false,cumulative_enabled:false,confirm:true});w.unmount()
})
it('does not permit an unsafe cumulative to reset downgrade',async()=>{
 vi.mocked(apiGet).mockResolvedValue({enabled:true,cumulative_enabled:true,cumulative_started:true,links:[{id:1,source_name:'DMIT',target_name:'Verizon',state:'prepared'}]})
 const w=mount(AdminRelayMetering);await flushPromises();expect(w.findAll('input')[1]!.attributes('disabled')).toBeDefined();expect(w.text()).toContain('等待落地接受');w.unmount()
})
it('shows failed load rather than claiming no links and cancellation sends nothing',async()=>{
 vi.mocked(apiGet).mockRejectedValue(new Error('unavailable'));const w=mount(AdminRelayMetering);await flushPromises();expect(w.text()).toContain('unavailable');expect(w.text()).not.toContain('尚无独立链路身份');w.unmount()
})
