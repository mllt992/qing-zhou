<template>
  <div>
    <!-- 打码开关放在标题行而不是某个页签里：它对整页生效（机器地址、出口地址、
         拓扑、配置预览），截图前点一次就够。也别放进 n-tabs 的 suffix——那会从
         页签栏里吃掉几十像素，375px 的手机上本来就装不下 5 个页签（可视 351px /
         需要 395px，且 overflow:hidden 滚不动），再挤就直接够不着最后一个了。 -->
    <div class="page-head">
      <div>
        <h2 class="page-title">sing-box 配置</h2>
        <p class="page-sub">统一管理机器、TLS、入站、代理出口与下发状态</p>
      </div>
      <n-button size="tiny" quaternary :type="showIp ? 'warning' : 'default'" @click="toggleIp">
        {{ showIp ? '🙈 隐藏 IP' : '👁 显示 IP' }}
      </n-button>
    </div>
    <div class="sb-overview" aria-label="sing-box 配置总览">
      <button type="button" @click="tab = 'topology'">
        <span class="sb-icon">机</span><span><b>{{ machines.length }}</b><small>运行机器 · 本机 {{ servers.length ? '+ 远程' : '' }}</small></span>
      </button>
      <button type="button" @click="tab = 'tls'">
        <span class="sb-icon mint">盾</span><span><b>{{ tlsList.length }}</b><small>TLS 配置 · 证书 {{ certList.length }}</small></span>
      </button>
      <button type="button" @click="tab = 'inbounds'">
        <span class="sb-icon amber">入</span><span><b>{{ enabledInboundCount }} / {{ inbounds.length }}</b><small>启用入站 / 全部入站</small></span>
      </button>
      <button type="button" @click="tab = 'egress'">
        <span class="sb-icon violet">出</span><span><b>{{ egresses.length }}</b><small>代理出口 · 绑定 {{ egressBoundCount }} 入站</small></span>
      </button>
      <button type="button" @click="tab = 'topology'">
        <span class="sb-icon" :class="syncFailedCount ? 'danger' : 'ok'">同步</span><span><b>{{ syncHealthyCount }} / {{ machines.length }}</b><small>{{ syncFailedCount ? `${syncFailedCount} 台下发失败` : '机器同步状态正常' }}</small></span>
      </button>
    </div>
    <n-tabs v-model:value="tab" animated @update:value="onTabChange">
      <n-tab-pane name="tls" tab="TLS 配置">
        <div class="page-toolbar">
          <n-input v-model:value="tlsSearch" placeholder="搜索名称/SNI" size="small" clearable style="width:200px;max-width:50%;" />
          <span class="spacer" />
          <n-button size="small" @click="toggleAllMachines">{{ allExpanded ? '全部折叠' : '全部展开' }}</n-button>
          <n-button size="small" type="primary" @click="openTls()">添加 TLS</n-button>
        </div>
        <n-spin :show="loading">
          <n-collapse v-if="tlsGroups.length" v-model:expanded-names="expandedMachines" arrow-placement="left" class="machine-list">
            <n-collapse-item v-for="g in tlsGroups" :key="g.machine.id" :name="g.machine.id">
              <template #header>
                <div class="machine-head">
                  <span class="machine-name">{{ g.machine.name }}</span>
                  <n-tag size="tiny" :type="g.machine.isLocal ? 'info' : 'default'" :bordered="false">{{ g.machine.isLocal ? '本机' : '远程' }}</n-tag>
                  <span class="machine-host">{{ dispHost(g.machine.host) }}</span>
                </div>
              </template>
              <template #header-extra>
                <div class="machine-extra" @click.stop>
                  <n-tag size="tiny" :type="g.total ? 'success' : 'default'" :bordered="false">{{ g.total }} 项</n-tag>
                  <n-button size="tiny" type="primary" @click="openTlsFor(g.machine.id)">＋ TLS</n-button>
                </div>
              </template>
              <div v-if="g.items.length" class="card-grid">
                <div v-for="(r, idx) in g.items" :key="r.id" class="list-card">
                  <div class="lc-head">
                    <span class="lc-title wrap" :title="r.name || ''">{{ r.name || '—' }}</span>
                    <n-tag :type="r.mode === 'reality' ? 'success' : 'info'" size="tiny" :bordered="false">{{ r.mode === 'reality' ? 'Reality' : '证书 TLS' }}</n-tag>
                    <n-tag v-if="r.cert_info" :type="r.cert_info.expired ? 'error' : r.cert_info.expiring ? 'warning' : 'success'" size="tiny" :bordered="false">
                      {{ r.cert_info.expired ? '已过期' : r.cert_info.days_left + '天' }}
                    </n-tag>
                  </div>
                  <div class="lc-meta">
                    <span class="kv">SNI <b>{{ jp(r.server_json).server_name || '—' }}</b></span>
                    <span class="kv">入站数 <b>{{ tlsUseCount(r.id) }}</b></span>
                  </div>
                  <div class="lc-foot">
                    <n-button size="tiny" @click="openTls(r)">编辑</n-button>
                    <n-button size="tiny" @click="cloneTls(r)">克隆</n-button>
                    <n-button size="tiny" type="error" @click="deleteTls(r.id)">删除</n-button>
                    <span class="lc-order">
                      <n-button size="tiny" quaternary :disabled="idx === 0 || reordering" title="上移" @click="moveTls(g, idx, -1)">↑</n-button>
                      <n-button size="tiny" quaternary :disabled="idx === g.items.length - 1 || reordering" title="下移" @click="moveTls(g, idx, 1)">↓</n-button>
                    </span>
                  </div>
                </div>
              </div>
              <n-empty v-else :description="tlsSearch ? '无匹配 TLS' : '该机器暂无 TLS'" size="small" style="padding:18px 0;">
                <template v-if="!tlsSearch" #extra><n-button size="tiny" @click="openTlsFor(g.machine.id)">添加 TLS</n-button></template>
              </n-empty>
            </n-collapse-item>
          </n-collapse>
          <n-empty v-else-if="!loading" :description="tlsSearch ? '无匹配 TLS' : '暂无 TLS 配置'" style="padding:40px 0;" />
        </n-spin>
      </n-tab-pane>

      <n-tab-pane name="inbounds" tab="入站">
        <div class="page-toolbar">
          <n-input v-model:value="inbSearch" placeholder="搜索 tag/协议" size="small" clearable style="width:160px;max-width:40%;" />
          <n-select v-model:value="presetType" :options="presetOpts" placeholder="一键模板" size="small" style="width:180px;" @update:value="applyPreset" />
          <span class="spacer" />
          <n-button v-if="checkedIds.size" size="small" @click="batchToggle(true)">批量启用</n-button>
          <n-button v-if="checkedIds.size" size="small" @click="batchToggle(false)">批量停用</n-button>
          <n-button v-if="checkedIds.size" size="small" type="error" @click="batchDelete">批量删除</n-button>
          <n-button size="small" @click="toggleAllMachines">{{ allExpanded ? '全部折叠' : '全部展开' }}</n-button>
          <n-button size="small" type="primary" @click="openInbound()">添加入站</n-button>
        </div>
        <n-spin :show="loading">
          <n-collapse v-if="inboundGroups.length" v-model:expanded-names="expandedMachines" arrow-placement="left" class="machine-list">
            <n-collapse-item v-for="g in inboundGroups" :key="g.machine.id" :name="g.machine.id">
              <template #header>
                <div class="machine-head">
                  <span class="machine-name">{{ g.machine.name }}</span>
                  <n-tag size="tiny" :type="g.machine.isLocal ? 'info' : 'default'" :bordered="false">{{ g.machine.isLocal ? '本机' : '远程' }}</n-tag>
                  <span class="machine-host">{{ dispHost(g.machine.host) }}</span>
                  <n-tag v-if="!g.machine.enabled" size="tiny" type="warning" :bordered="false">已禁用</n-tag>
                </div>
              </template>
              <template #header-extra>
                <div class="machine-extra" @click.stop>
                  <n-tag size="tiny" :type="g.enabledCount ? 'success' : 'default'" :bordered="false">启用 {{ g.enabledCount }} / {{ g.total }}</n-tag>
                  <n-button size="tiny" @click="previewMachine(g.machine.id)">预览</n-button>
                  <n-button size="tiny" type="primary" @click="openInboundFor(g.machine.id)">＋ 入站</n-button>
                </div>
              </template>
              <div v-if="g.items.length" class="card-grid">
                <div v-for="(r, idx) in g.items" :key="r.id" class="list-card">
                  <div class="lc-head">
                    <n-checkbox :checked="checkedIds.has(r.id)" @update:checked="toggleCheck(r.id)" style="margin-right:6px;" />
                    <span class="lc-title wrap" :title="r.tag || ''">{{ r.tag || '—' }}</span>
                    <n-tag :type="r.enabled ? 'success' : 'error'" size="tiny" :bordered="false" style="cursor:pointer;" @click="toggleInbound(r)">{{ r.enabled ? '启用' : '停用' }}</n-tag>
                  </div>
                  <div class="lc-meta">
                    <span class="kv"><n-tag size="tiny" :bordered="false">{{ (r.type || '').toUpperCase() }}</n-tag></span>
                    <span class="kv">端口 <b>{{ r.listen_port }}</b></span>
                    <span class="kv">用户 <b>{{ r.user_count ?? 0 }}</b></span>
                    <span class="kv">TLS <b :title="tlsName(r.tls_id)">{{ tlsName(r.tls_id) }}</b></span>
                    <span v-if="r.egress_id" class="kv">出口 <b :title="egressName(r.egress_id)">{{ egressName(r.egress_id) }}</b></span>
                  </div>
                  <div class="lc-foot">
                    <n-button size="tiny" @click="openInbound(r)">编辑</n-button>
                    <n-button size="tiny" @click="cloneInbound(r)">克隆</n-button>
                    <n-button size="tiny" :loading="portChecking === r.id" @click="checkPort(r)">测端口</n-button>
                    <n-button size="tiny" type="error" @click="deleteInbound(r.id)">删除</n-button>
                    <span class="lc-order">
                      <n-button size="tiny" quaternary :disabled="idx === 0 || reordering" title="上移" @click="moveInbound(g, idx, -1)">↑</n-button>
                      <n-button size="tiny" quaternary :disabled="idx === g.items.length - 1 || reordering" title="下移" @click="moveInbound(g, idx, 1)">↓</n-button>
                    </span>
                  </div>
                  <div v-if="portResult[r.id]" class="port-result" :class="{ ok: portResult[r.id].reachable, err: !portResult[r.id].reachable }">
                    {{ portResult[r.id].reachable ? '可达 · ' + portResult[r.id].ms.toFixed(0) + 'ms' : '不可达 · ' + (portResult[r.id].error || '失败') }}
                  </div>
                </div>
              </div>
              <n-empty v-else :description="inbSearch ? '无匹配入站' : '该机器暂无入站'" size="small" style="padding:18px 0;">
                <template v-if="!inbSearch" #extra><n-button size="tiny" @click="openInboundFor(g.machine.id)">添加入站</n-button></template>
              </n-empty>
            </n-collapse-item>
          </n-collapse>
          <n-empty v-else-if="!loading" :description="inbSearch ? '无匹配入站' : '暂无入站'" style="padding:40px 0;" />
        </n-spin>
      </n-tab-pane>

      <n-tab-pane name="egress" tab="代理出口">
        <div class="page-toolbar">
          <span class="spacer" />
          <n-button size="small" @click="openEgressImport()">粘贴导入</n-button>
          <n-button size="small" type="primary" @click="openEgress()">添加出口</n-button>
        </div>
        <n-spin :show="loading">
          <div v-if="egresses.length" class="card-grid">
            <div v-for="r in egresses" :key="r.id" class="list-card">
              <div class="lc-head">
                <span class="lc-title wrap" :title="r.name">{{ r.name }}</span>
                <n-tag size="tiny" type="info" :bordered="false">{{ (r.type || '').toUpperCase() }}</n-tag>
                <n-tag v-if="r.tls_enabled" size="tiny" :type="r.tls_insecure ? 'warning' : 'success'" :bordered="false">
                  {{ r.tls_insecure ? 'TLS·不校验' : 'TLS' }}
                </n-tag>
              </div>
              <div class="lc-meta">
                <span class="kv">地址 <b>{{ dispHost(r.host) }}:{{ r.port }}</b></span>
                <span class="kv">用户名 <b>{{ r.username || '—' }}</b></span>
                <span v-if="r.tls_enabled && r.sni" class="kv">SNI <b>{{ r.sni }}</b></span>
                <span class="kv">UDP <b>{{ r.udp_mode_effective === 'block' ? '阻断' : '透传' }}</b></span>
                <span class="kv">超时 <b>{{ r.connect_timeout_effective_ms }}ms</b></span>
                <span class="kv">入站数 <b>{{ r.inbound_count ?? 0 }}</b></span>
              </div>
              <div v-if="r._test" class="egress-test" :class="r._test.ok ? 'ok' : 'err'">
                <template v-if="r._test.mode === 'probe'">
                  <template v-if="r._test.ok_count + r._test.fail_count === 0">
                    ❓ 并发探测没有拿到结果（经 {{ r._test.via_server }}）：{{ dispText(r._test.output) || '节点无输出' }}
                  </template>
                  <template v-else>
                    <template v-if="r._test.ok">✅ 并发 {{ r._test.requested }} 条全部成功</template>
                    <template v-else>⚠️ 并发 {{ r._test.requested }} 条：成功 <b>{{ r._test.ok_count }}</b> / 失败 <b>{{ r._test.fail_count }}</b></template>
                    <template v-if="r._test.latency_max_ms">
                      · 延迟 {{ r._test.latency_min_ms }}/{{ r._test.latency_p50_ms }}/{{ r._test.latency_max_ms }}ms（最小/中位/最大）
                    </template>
                    · 经 {{ r._test.via_server }}
                    <div v-for="(e, i) in (r._test.errors || [])" :key="i">× {{ e.count }} 次：{{ dispText(e.msg) }}</div>
                    <div v-if="r._test.fail_count > 0" class="probe-hint">
                      部分失败通常是供应商的并发连接数上限——所有绑定此出口的入站共用一个账号，浏览网页一次就会开几十条连接。
                      但先看上面的错误信息：出现 <b>429</b> 或 rate limit 字样的是 IP 回显服务在限流，不是你的出口到顶了。
                    </div>
                  </template>
                </template>
                <template v-else-if="r._test.ok">✅ 出口 IP <b>{{ dispHost(r._test.ip) }}</b> · {{ r._test.latency_ms }}ms · 经 {{ r._test.via_server }}</template>
                <template v-else>❌ 不通（经 {{ r._test.via_server }}）：{{ dispText(r._test.output) }}</template>
              </div>
              <div class="lc-foot">
                <n-button size="tiny" :loading="r._testing" @click="testEgress(r)">测试连通</n-button>
                <n-button size="tiny" :loading="r._probing" @click="probeEgress(r)">并发探测</n-button>
                <n-button size="tiny" :loading="r._cloning" @click="cloneEgress(r)">克隆</n-button>
                <n-button size="tiny" @click="openEgress(r)">编辑</n-button>
                <n-button size="tiny" type="error" @click="deleteEgress(r.id)">删除</n-button>
              </div>
            </div>
          </div>
          <n-empty v-else-if="!loading" description="暂无代理出口。购买的静态 IP（SOCKS5 / HTTP 代理）在这里录入，之后在入站上选择它作为出口即可。" style="padding:40px 0;" />
        </n-spin>
      </n-tab-pane>

      <n-tab-pane name="topology" tab="链路拓扑">
        <n-spin :show="loading">
          <div v-if="inbounds.length" class="topo">
            <div class="topo-legend">
              <span><i class="dot client"></i>客户端</span>
              <span><i class="dot entry"></i>入口 / 线路机入站</span>
              <span><i class="dot landing"></i>落地入站</span>
              <span><i class="dot egress"></i>代理出口</span>
              <span><i class="dot inet"></i>互联网</span>
            </div>
            <div v-for="g in inboundGroups" :key="g.machine.id" class="topo-machine">
              <div class="topo-mhead">
                <span class="machine-name">{{ g.machine.name }}</span>
                <n-tag size="tiny" :type="g.machine.isLocal ? 'info' : 'default'" :bordered="false">{{ g.machine.isLocal ? '本机' : '远程' }}</n-tag>
                <span class="machine-host">{{ dispHost(g.machine.host) }}</span>
                <n-tag v-if="syncBadge(g.machine.id)" size="tiny" :type="syncBadge(g.machine.id)!.type" :bordered="false">
                  {{ syncBadge(g.machine.id)!.text }}
                </n-tag>
                <span v-if="syncBadge(g.machine.id)" class="sync-time">{{ agoText(syncAt(g.machine.id)) }}</span>
                <span style="flex:1;"></span>
                <n-button size="tiny" quaternary :loading="resyncing === g.machine.id" @click="resync(g.machine.id)">重新下发</n-button>
              </div>
              <!-- 失败原因必须直接摆出来：一个光秃秃的「下发失败」徽标等于没说，
                   管理员没有别的地方能看到这条错误（面板日志在服务器上）。 -->
              <div v-if="syncError(g.machine.id) !== null" class="sync-err">
                <b>下发失败</b>：{{ dispText(syncError(g.machine.id) || '') || '未记录具体原因——常见于 SSH 不可达、节点上 sing-box 校验不通过。点右上角「重新下发」可重试并拿到本次的错误。' }}
              </div>
              <div v-for="r in g.items" :key="r.id" class="topo-row" :class="{ off: !r.enabled }">
                <span class="topo-node client">👤 客户端</span>
                <span class="topo-arrow">→</span>
                <span class="topo-node entry">
                  <b>{{ r.tag }}</b>
                  <span class="topo-proto">{{ (r.type || '').toUpperCase() }}</span>
                  <span class="topo-port">:{{ r.listen_port }}</span>
                </span>
                <template v-for="(seg, si) in chainOf(r)" :key="si">
                  <template v-if="seg.kind === 'landing'">
                    <span class="topo-arrow relay">⇢ 中转 ⇢</span>
                    <span class="topo-node landing">
                      <b>{{ seg.ib.tag }}</b>
                      <span class="topo-proto">{{ (seg.ib.type || '').toUpperCase() }}</span>
                      <span class="topo-loc">@ {{ serverName(seg.ib.server_id) }}</span>
                    </span>
                  </template>
                  <template v-else-if="seg.kind === 'egress'">
                    <span class="topo-arrow egress">⇢ 代理出口 ⇢</span>
                    <span class="topo-node egress">
                      <b>{{ seg.eg.name }}</b>
                      <span class="topo-proto">{{ (seg.eg.type || '').toUpperCase() }}</span>
                      <span class="topo-loc">{{ dispHost(seg.eg.host) }}</span>
                    </span>
                  </template>
                  <span v-else-if="seg.kind === 'broken-landing'" class="topo-arrow relay warn">⇢ 落地已失效 ⇢</span>
                  <span v-else-if="seg.kind === 'broken-egress'" class="topo-arrow relay warn">⇢ 出口已失效 ⇢</span>
                </template>
                <span class="topo-arrow">→</span>
                <span class="topo-node inet">🌐 互联网</span>
                <span class="topo-actions">
                  <n-button v-if="!r.upstream_inbound_id && !r.egress_id" class="primary-quiet" size="tiny" quaternary type="primary" @click="addLandingAfter(r)">＋ 串联落地</n-button>
                  <n-popselect v-if="!r.upstream_inbound_id && !r.egress_id && egresses.length" :options="egressPopOpts" class="qz-wide-menu" scrollable @update:value="(v: number) => attachEgress(r, v)">
                    <n-button class="primary-quiet" size="tiny" quaternary type="primary">＋ 挂出口</n-button>
                  </n-popselect>
                  <n-button v-else-if="r.upstream_inbound_id" size="tiny" quaternary type="warning" @click="unlinkRelay(r)">解除中转</n-button>
                  <n-button v-else-if="r.egress_id" size="tiny" quaternary type="warning" @click="unlinkEgress(r)">解除出口</n-button>
                </span>
              </div>
            </div>
          </div>
          <n-empty v-else-if="!loading" description="暂无入站，无法展示链路" style="padding:40px 0;" />
        </n-spin>
      </n-tab-pane>

      <n-tab-pane name="preview" tab="配置预览">
        <div class="page-toolbar">
          <n-select v-model:value="previewSid" :options="serverOpts" v-bind="longSelFor(serverOpts.length)" placeholder="本机" clearable style="width:200px;max-width:60%;" size="small" @update:value="onPreviewServerChange" />
          <span class="spacer" />
          <n-button size="small" :loading="checkLoading" :disabled="!previewJson" @click="runCheck">正确性检查</n-button>
          <n-button size="small" :disabled="!previewJson" @click="copyPreview">复制配置</n-button>
          <n-button size="small" type="primary" :loading="previewLoading" @click="loadPreview">刷新预览</n-button>
        </div>
        <!-- 这条提醒不能省：这一页把打码开关做出来，就会有人以为「打了码=可以截图」。
             打码只处理地址，配置里的 Reality 私钥、SS/Trojan 密码、用户 UUID 全是明文。 -->
        <p v-if="previewJson" style="font-size:12px;color:var(--warning,#d97706);margin:0 0 8px;line-height:1.7;">
          <template v-if="!showIp">IP 已按打码显示（右上角可切换），「复制配置」复制的仍是<b>真实</b>配置。</template>
          ⚠️ 打码只管地址：这份配置里的 <b>Reality 私钥、Shadowsocks/Trojan 密码、用户 UUID</b> 仍是明文，
          外发前请自行删改，别整页截图。
        </p>
        <p v-if="previewNoInbounds" style="font-size:12px;color:var(--warning,#d97706);margin:0 0 10px;line-height:1.7;">
          这台机器（{{ serverName(previewSid || 0) }}）下没有任何入站，因此配置里 <code>inbounds</code> 为空。
          请在上方下拉切换到入站所在的机器，或回「入站」页确认入站的**归属机器**与**已启用**状态。
        </p>
        <div v-if="checkResult" class="check-result" :class="checkResult.ok ? 'ok' : 'err'">
          <div class="check-head">
            <b>{{ checkResult.ok ? '✓ 配置校验通过' : '✗ 配置存在问题' }}</b>
            <span class="check-meta">入站 {{ checkResult.inbounds }} · 出站 {{ checkResult.outbounds }}<template v-if="checkResult.stage === 'no-binary'"> · 未做 sing-box check</template></span>
          </div>
          <ul v-if="checkResult.warnings && checkResult.warnings.length" class="check-warn">
            <li v-for="(wmsg, i) in checkResult.warnings" :key="i">{{ wmsg }}</li>
          </ul>
          <pre v-if="checkResult.output" class="check-out">{{ checkResult.output }}</pre>
        </div>
        <n-code :code="previewShown" language="json" style="max-height:60vh;overflow:auto;" />
      </n-tab-pane>
    </n-tabs>

    <!-- TLS 编辑抽屉 -->
    <n-drawer v-model:show="showTls" :width="drawerW" placement="right">
      <n-drawer-content :title="te.id ? '编辑 TLS' : '添加 TLS'" closable>
        <n-form label-placement="left" label-width="100">
          <n-form-item label="类型">
            <n-radio-group v-model:value="te.mode" :disabled="!!te.id">
              <n-radio value="reality">Reality</n-radio>
              <n-radio value="tls">证书 TLS</n-radio>
            </n-radio-group>
          </n-form-item>
          <n-form-item label="名称"><n-input v-model:value="te.name" /></n-form-item>
          <n-form-item label="所属服务器"><n-select v-model:value="te.server_id" :options="serverOpts" v-bind="longSelFor(serverOpts.length)" placeholder="本机" clearable /></n-form-item>
          <n-form-item label="SNI 伪装域名">
            <n-input-group>
              <n-input v-model:value="te.server_name" placeholder="www.microsoft.com" style="flex:1;" />
              <n-button :loading="sniTesting" @click="testSni">测试延迟</n-button>
            </n-input-group>
          </n-form-item>
          <n-form-item label=" " v-if="!te.id">
            <n-select :options="sniPresets" placeholder="选择常用 SNI 预设" size="small" @update:value="v => te.server_name = v" />
          </n-form-item>
          <n-form-item v-if="sniResult" label=" ">
            <div class="sni-result" :class="sniResult.status">
              <span v-if="sniResult.status === 'ok'" class="ok">
                <b>连通</b> · 平均 {{ sniResult.avg_ms.toFixed(0) }}ms · 最小 {{ sniResult.min_ms.toFixed(0) }}ms · 最大 {{ sniResult.max_ms.toFixed(0) }}ms ({{ sniResult.ok }}/{{ sniResult.total }})
              </span>
              <span v-else-if="sniResult.status === 'partial'" class="warn">
                <b>不稳定</b> · 平均 {{ sniResult.avg_ms.toFixed(0) }}ms · {{ sniResult.ok }}/{{ sniResult.total }} 次成功
              </span>
              <span v-else class="err"><b>不可达</b> · {{ sniResult.samples?.[0]?.error || '连接失败' }}</span>
            </div>
          </n-form-item>
          <n-form-item label="uTLS 指纹"><n-select v-model:value="te.fingerprint" :options="fpOpts" /></n-form-item>
          <template v-if="te.mode === 'reality'">
            <n-form-item label="握手目标">
              <n-input-group>
                <n-input v-model:value="te.handshake_server" placeholder="留空=同 SNI" style="flex:1;" />
                <n-button :loading="hsTesting" @click="testHandshake">测握手</n-button>
              </n-input-group>
            </n-form-item>
            <n-form-item label="握手端口"><n-input-number v-model:value="te.handshake_port" :min="1" :max="65535" style="width:100%;" /></n-form-item>
            <n-form-item v-if="hsResult" label=" ">
              <div class="sni-result" :class="hsResult.status">
                <span v-if="hsResult.status === 'ok'" class="ok"><b>连通</b> · 平均 {{ hsResult.avg_ms.toFixed(0) }}ms ({{ hsResult.ok }}/{{ hsResult.total }})</span>
                <span v-else-if="hsResult.status === 'partial'" class="warn"><b>不稳定</b> · {{ hsResult.ok }}/{{ hsResult.total }} 次成功</span>
                <span v-else class="err"><b>不可达</b> · {{ hsResult.samples?.[0]?.error || '连接失败' }}</span>
              </div>
            </n-form-item>
            <n-form-item>
              <n-button type="primary" @click="genKeys" :loading="genLoading">一键生成 Reality 密钥对</n-button>
            </n-form-item>
            <n-form-item label="私钥"><n-input :value="te.private_key" readonly placeholder="点击上方按钮生成" @click="copy(te.private_key)" style="cursor:pointer;" /></n-form-item>
            <n-form-item label="公钥"><n-input :value="te.public_key" readonly placeholder="点击上方按钮生成" @click="copy(te.public_key)" style="cursor:pointer;" /></n-form-item>
            <n-form-item label="Short ID">
              <div style="display:flex;flex-direction:column;gap:6px;width:100%;">
                <div v-for="(_, i) in te.short_ids" :key="i" style="display:flex;gap:6px;">
                  <n-input v-model:value="te.short_ids[i]" placeholder="自动生成" style="flex:1;" />
                  <n-button size="tiny" quaternary @click="te.short_ids.splice(i, 1)">✕</n-button>
                </div>
                <n-button size="tiny" dashed @click="te.short_ids.push('')">+ 添加 Short ID</n-button>
              </div>
            </n-form-item>
          </template>
          <template v-if="te.mode === 'tls'">
            <n-form-item label="引用证书">
              <n-select :value="te.cert_id" :options="certOpts" v-bind="longSelFor(certOpts.length)" @update:value="onCertPick" />
            </n-form-item>
            <n-form-item v-if="te.cert_id" label=" ">
              <n-tag type="success" size="small" style="white-space:normal;height:auto;">已引用「证书管理」中的证书：SNI 自动取证书域名，真实证书自动关闭「允许不安全」；续期与管理请到证书管理页。</n-tag>
            </n-form-item>
            <template v-if="!te.cert_id">
            <n-form-item label=" ">
              <div style="display:flex;flex-direction:column;gap:4px;width:100%;">
                <n-button :loading="genCertLoading" @click="genSelfSigned">一键生成自签证书（按 SNI）</n-button>
                <span style="font-size:11px;color:var(--text-3);">自签证书适用于 TUIC / Hysteria2 等允许 insecure 或证书指纹的客户端。需可信证书建议到「证书管理」页申请后在上方引用。</span>
              </div>
            </n-form-item>
            <n-form-item v-if="te.server_id === 0" label=" ">
              <n-collapse class="acme-collapse" style="width:100%;">
                <n-collapse-item name="acme">
                  <template #header><span style="font-size:13px;font-weight:600;">ACME 在线申请真实证书（可选 · Let's Encrypt）</span></template>
                  <div style="display:flex;flex-direction:column;gap:6px;padding-top:2px;">
                    <span style="font-size:11px;color:var(--text-3);">仅套 CDN 的 WS-TLS 等场景才需要真证书；仅本机可申请。</span>
                    <n-radio-group v-model:value="acme.method" size="small">
                      <n-radio value="dns-cf">Cloudflare DNS（推荐，支持泛域名、无需端口）</n-radio>
                      <n-radio value="webroot">Webroot（nginx/网站根目录，不占端口）</n-radio>
                      <n-radio value="http-01">HTTP-01 standalone（需 80 端口空闲）</n-radio>
                    </n-radio-group>
                    <n-input v-if="acme.method === 'dns-cf'" v-model:value="acme.cf_token" type="password" show-password-on="click" placeholder="Cloudflare API Token" />
                    <n-input v-if="acme.method === 'webroot'" v-model:value="acme.webroot" placeholder="网站根目录，如 /var/www/html（nginx 该域名 root）" />
                    <span v-if="acme.method === 'http-01'" style="font-size:11px;color:var(--text-3);">若本机已用 nginx 占用 80 端口，standalone 会失败——请改用 Cloudflare DNS 或 Webroot。</span>
                    <n-input v-model:value="acme.email" placeholder="账户邮箱（可选，建议填写）" />
                    <n-button type="primary" :loading="acmeLoading" @click="requestAcme">申请证书（域名取上方 SNI，名称取上方名称）</n-button>
                    <span style="font-size:11px;color:var(--text-3);">申请成功后证书写入本机固定路径，sing-box 以 certificate_path 引用；续期由 acme.sh 的 cron 自动完成。远程服务器暂不支持在线申请。</span>
                  </div>
                </n-collapse-item>
              </n-collapse>
            </n-form-item>
            <n-form-item label="证书 PEM"><n-input v-model:value="te.certificate" type="textarea" :rows="3" placeholder="-----BEGIN CERTIFICATE-----" /></n-form-item>
            <n-form-item label="私钥 PEM"><n-input v-model:value="te.key" type="textarea" :rows="3" placeholder="-----BEGIN PRIVATE KEY-----" /></n-form-item>
            </template>
            <n-form-item label="ALPN">
              <n-select v-model:value="te.alpn" :options="[{label:'h3',value:'h3'},{label:'h2',value:'h2'},{label:'http/1.1',value:'http/1.1'}]" multiple />
            </n-form-item>
            <n-form-item label="最低 TLS"><n-select v-model:value="te.min_version" :options="verOpts" clearable /></n-form-item>
            <n-form-item label="最高 TLS"><n-select v-model:value="te.max_version" :options="verOpts" clearable /></n-form-item>
            <n-form-item label="允许不安全"><n-switch v-model:value="te.insecure" /></n-form-item>
          </template>
        </n-form>
        <n-button type="primary" block :loading="saving" @click="saveTls">保存</n-button>
      </n-drawer-content>
    </n-drawer>

    <!-- 代理出口编辑抽屉 -->
    <n-drawer v-model:show="showEg" :width="drawerW" placement="right">
      <n-drawer-content :title="ee.id ? '编辑代理出口' : '添加代理出口'" closable>
        <n-form label-placement="left" label-width="100">
          <n-form-item label="粘贴链接">
            <div style="width:100%;">
              <n-input-group>
                <n-input v-model:value="egParseText" placeholder="socks5://user:pass@1.2.3.4:1080 或 1.2.3.4:1080:user:pass" @keyup.enter="parseIntoForm" />
                <n-button :loading="egParsing" @click="parseIntoForm">解析填充</n-button>
              </n-input-group>
              <div class="form-tip">把供应商给的那串直接贴进来，自动拆到下面各字段。支持 <code>socks5://</code>／<code>http(s)://</code>／<code>user:pass@host:port</code>／<code>host:port:user:pass</code>／<code>host:port</code>。多条批量导入请用列表页的「粘贴导入」。</div>
            </div>
          </n-form-item>
          <n-form-item label="名称"><n-input v-model:value="ee.name" placeholder="如：静态IP-香港" /></n-form-item>
          <n-form-item label="类型">
            <div style="width:100%;">
              <n-radio-group :value="ee.type" @update:value="onEgressType">
                <n-radio value="socks">SOCKS5</n-radio>
                <n-radio value="http">HTTP</n-radio>
              </n-radio-group>
              <div class="form-tip">按供应商给的协议选，选错的表现是连上后立刻被 RST（对端读不懂握手包）。高位随机端口通常是 SOCKS5。</div>
            </div>
          </n-form-item>
          <n-form-item label="服务器地址"><n-input v-model:value="ee.host" placeholder="IP 或域名" /></n-form-item>
          <n-form-item label="端口"><n-input-number v-model:value="ee.port" :min="1" :max="65535" style="width:100%;" /></n-form-item>
          <n-form-item label="用户名"><n-input v-model:value="ee.username" placeholder="无认证则留空" /></n-form-item>
          <n-form-item label="密码"><n-input v-model:value="ee.password" type="password" show-password-on="click" :placeholder="ee.id ? '*** 表示保持原密码不变' : '无认证则留空'" /></n-form-item>
          <n-form-item label="UDP 策略">
            <div style="width:100%;">
              <n-radio-group :value="ee.udp_mode" @update:value="(v: string) => ee.udp_mode = v">
                <n-radio value="">自动（按类型）</n-radio>
                <n-radio value="passthrough" :disabled="ee.type === 'http'">透传给代理</n-radio>
                <n-radio value="block">阻断 UDP</n-radio>
              </n-radio-group>
              <div class="form-tip">
                当前生效：<b>{{ effectiveUdpMode === 'block' ? '阻断' : '透传' }}</b>。
                <template v-if="ee.type === 'http'">HTTP 出口不能透传 UDP——sing-box 的 http 出站没有 UDP 通路。</template>
                <template v-else>多数商用静态 IP 并不真的中转 UDP。</template><br>
                <b>「阻断」买到的是确定性，不是提速</b>：实测（sing-box 1.13.18）阻断是在服务端把 UDP 丢掉，客户端只会超时，
                不会收到明确拒绝、也不会因此更快回落 TCP。它的价值在于——代理「半通」的 UDP 比完全没有更糟：
                QUIC 能握上手却中途卡死，浏览器反而不回落。阻断把「时好时坏」变成「一直没有」，客户端就知道该走 TCP。
                代价是经此出口的入站不能玩 UDP 游戏／音视频。真要让浏览器秒回落，得在客户端订阅配置里禁 UDP/443。<br>
                <b>DNS 不在阻断范围内</b>：选「阻断」时，面板会自动让节点就地应答该入站的 UDP:53 查询。
                否则客户端的 DNS（也是 UDP）会跟着一起死——用 Clash／sing-box 订阅的人因为走 DoH 察觉不到，
                而 v2rayN／v2rayNG 拿的是裸链接、默认走 UDP DNS，会变成「什么都打不开」。
                若你在「sing-box 基础配置」里自己写过 DNS 规则，则以你的为准，面板不再插入。
              </div>
            </div>
          </n-form-item>
          <n-form-item label="连接超时">
            <div style="width:100%;">
              <n-input-number v-model:value="ee.connect_timeout_ms" :min="0" :max="60000" :step="500" style="width:100%;" />
              <div class="form-tip">
                当前生效：<b>{{ ee.connect_timeout_ms > 0 ? ee.connect_timeout_ms : 5000 }}ms</b>（填 0 表示用默认值 5000，不是「不限时」）。<br>
                这是连到代理的 <b>TCP 握手</b>上限。代理被停用时往往是不回包而不是拒绝，不设上限每条连接都要卡满系统级 TCP 超时（Linux 约 2 分钟），页面既不加载也不报错。
                握手完成后卡在 SOCKS5／CONNECT 阶段的情况它管不了——那种要用「并发探测」看。
              </div>
            </div>
          </n-form-item>
          <n-form-item label="TLS 加密">
            <div style="width:100%;">
              <n-switch v-model:value="ee.tls_enabled" :disabled="ee.type !== 'http'" />
              <div class="form-tip">
                <template v-if="ee.type !== 'http'">仅 HTTP 类型可用——sing-box 的 SOCKS5 出站没有 TLS 选项。</template>
                <template v-else>即「HTTPS 代理」：到代理这一跳走 TLS，认证凭据不再明文过网。<b>端口是 443 不等于要开</b>，很多代理只是借 443 穿墙，实际是明文；开错的表现是握手阶段报证书错误。</template>
              </div>
            </div>
          </n-form-item>
          <template v-if="ee.tls_enabled && ee.type === 'http'">
            <n-form-item label="SNI">
              <div style="width:100%;">
                <n-input v-model:value="ee.sni" placeholder="证书上的域名，留空则用上面的地址" />
                <div class="form-tip">地址填的是 IP 时几乎一定要填：证书签给的是域名，不填会报 <code>doesn't contain any IP SANs</code>。</div>
              </div>
            </n-form-item>
            <n-form-item label="信任证书">
              <div style="width:100%;">
                <n-select v-model:value="ee.tls_cert_id" :options="egressTrustOpts" v-bind="longSelFor(egressTrustOpts.length)" />
                <div class="form-tip">
                  这一跳面板是<b>客户端</b>，选的证书只用来<b>校验对方</b>，不会发出去。<br>
                  商用代理保持「系统根证书」；只有代理是你自己搭的、用的正是证书管理里这张证书（含自签）时才需要指定。
                </div>
              </div>
            </n-form-item>
            <n-form-item label="跳过证书校验">
              <div style="width:100%;">
                <n-switch v-model:value="ee.tls_insecure" />
                <div class="form-tip" :style="ee.tls_insecure ? 'color:var(--warn,#d97706);' : ''">
                  仅用于临时排障。凭据就在这条 TLS 里，跳过校验等于允许中间人拿走它们并盗用你的出口。
                </div>
              </div>
            </n-form-item>
          </template>
        </n-form>
        <n-button type="primary" block :loading="saving" @click="saveEgress">保存</n-button>
      </n-drawer-content>
    </n-drawer>

    <!-- 代理出口批量粘贴导入 -->
    <n-drawer v-model:show="showEgImport" :width="drawerW" placement="right">
      <n-drawer-content title="粘贴导入代理出口" closable>
        <n-input
          v-model:value="egImportText" type="textarea" :rows="10"
          placeholder="一行一条，支持：&#10;socks5://user:pass@1.2.3.4:1080&#10;http://user:pass@1.2.3.4:8080&#10;1.2.3.4:1080:user:pass&#10;user:pass@1.2.3.4:1080&#10;1.2.3.4:1080&#10;以 # 开头的行会被忽略"
        />
        <div class="form-tip" style="margin:8px 0;">解析只在本地预览，确认无误再导入。没写协议的行会按端口猜类型，猜错的表现是连上后立刻被 RST——导入后请核对。</div>
        <n-button block :loading="egImportParsing" @click="parseEgressImport">解析</n-button>

        <template v-if="egImportErrors.length">
          <n-alert type="warning" :show-icon="false" style="margin-top:12px;">
            <div v-for="(e, i) in egImportErrors" :key="i" style="font-size:12px; word-break:break-all;">
              第 {{ e.line }} 行无法解析：{{ e.error }}<template v-if="e.text"> —— <code>{{ e.text }}</code></template>
            </div>
          </n-alert>
        </template>

        <template v-if="egImportItems.length">
          <div class="import-head">解析出 {{ egImportItems.length }} 条：</div>
          <div v-for="(it, i) in egImportItems" :key="i" class="import-row">
            <n-input v-model:value="it.name" size="small" placeholder="名称" style="max-width:190px;" />
            <n-tag size="tiny" :type="it.type_guessed ? 'warning' : 'info'" :bordered="false">
              {{ (it.type || '').toUpperCase() }}<template v-if="it.type_guessed">?</template>
            </n-tag>
            <n-tag v-if="it.tls_enabled" size="tiny" type="success" :bordered="false">TLS</n-tag>
            <span class="import-addr">{{ it.host }}:{{ it.port }}</span>
            <span class="import-user">{{ it.username || '无认证' }}</span>
          </div>
          <div v-if="egImportItems.some((x: any) => x.type_guessed)" class="form-tip" style="margin:6px 0;">
            带 <b>?</b> 的类型是按端口猜的，请对照供应商说明确认。
          </div>
          <n-button type="primary" block :loading="egImporting" style="margin-top:10px;" @click="doEgressImport">
            导入这 {{ egImportItems.length }} 条
          </n-button>
        </template>
      </n-drawer-content>
    </n-drawer>

    <!-- 入站编辑抽屉 -->
    <n-drawer v-model:show="showInb" :width="drawerW" placement="right">
      <n-drawer-content :title="ie.id ? '编辑入站' : '添加入站'" closable>
        <n-form label-placement="left" label-width="100">
          <n-alert v-if="chainSourceId" type="info" :show-icon="false" style="margin-bottom:12px;">
            新入站将作为「<b>{{ chainSourceName }}</b>」的落地，保存后自动建立中转链路（该入站→本入站→互联网）。
          </n-alert>
          <n-form-item label="协议"><n-select v-model:value="ie.type" :options="protoOpts" :disabled="!!ie.id" /></n-form-item>
          <n-alert v-if="ie.type === 'mixed'" type="warning" :show-icon="false" style="margin-bottom:12px;">
            <b>Mixed = HTTP + SOCKS5 普通代理</b>，同一端口两种都能用，可直接把「地址 / 端口 / 用户名 / 密码」填进 1Panel、Docker、git 等只认 HTTP/SOCKS 的地方（用户在「我的订阅」页复制）。
            <div style="margin-top:6px;">⚠️ 不挂 TLS 时账号密码与流量<b>均为明文</b>，公网使用请为其绑定一个<b>普通证书 TLS</b>（→ 变成 HTTPS 代理），或用防火墙限制来源 IP。不支持 Reality。</div>
          </n-alert>
          <n-form-item label="名称 / Tag"><n-input v-model:value="ie.tag" /></n-form-item>
          <n-form-item label="监听地址"><n-select v-model:value="ie.listen" :options="listenOpts" /></n-form-item>
          <n-form-item label="监听端口"><n-input-number v-model:value="ie.listen_port" :min="1" :max="65535" style="width:100%;" /></n-form-item>
          <n-form-item label="所属服务器"><n-select v-model:value="ie.server_id" :options="serverOpts" v-bind="longSelFor(serverOpts.length)" placeholder="本机" clearable /></n-form-item>
          <n-form-item label="落地 / 中转">
            <div style="width:100%;">
              <n-select v-model:value="ie.upstream_inbound_id" :options="landingOpts" v-bind="longSelFor(landingOpts.length)" @update:value="(v: number) => { if (v) ie.egress_id = 0 }" />
              <div class="form-tip">直接出网＝本入站即落地机；选择某入站＝本入站作为线路机，流量转发到该落地入站再出网。</div>
            </div>
          </n-form-item>
          <n-form-item label="代理出口">
            <div style="width:100%;">
              <n-select v-model:value="ie.egress_id" :options="egressOpts" v-bind="longSelFor(egressOpts.length)" @update:value="(v: number) => { if (v) ie.upstream_inbound_id = 0 }" />
              <div class="form-tip">选择后本入站的流量经该 SOCKS5/HTTP 代理（如购买的静态 IP）出网，出口 IP 即代理的 IP；与「落地 / 中转」二选一。出口在「代理出口」页管理。</div>
              <n-alert v-if="selectedEgressUdp === 'block'" type="warning" :show-icon="false" style="margin-top:8px;">
                所选出口的 <b>UDP 策略为「阻断」</b>：本入站的 UDP 会在服务端被丢弃（客户端表现为超时），TUIC / Hysteria / QUIC、以及依赖 UDP 的游戏和音视频将连不上。
                <b>DNS 除外</b>——节点会就地应答 DNS 查询，客户端不必配 DoH 也能正常解析。
                <template v-if="selectedEgressType === 'http'">HTTP 出口只能这样——sing-box 的 http 出站没有 UDP 通路。若本入站需要 UDP，请改选 <b>SOCKS5</b> 出口。</template>
                <template v-else>要放开可在「代理出口」页把该出口改成「透传」——前提是供应商真的中转 UDP，半通比不通更糟。</template>
              </n-alert>
            </div>
          </n-form-item>
          <n-form-item v-if="ie.type !== 'shadowsocks'" label="TLS / Reality"><n-select v-model:value="ie.tls_id" :options="tlsOpts" v-bind="longSelFor(tlsOpts.length)" placeholder="无" clearable /></n-form-item>
          <n-form-item v-if="ie.type === 'mixed' && !ie.tls_id" label=" ">
            <div style="width:100%;">
              <n-button size="small" :loading="quickCertLoading" @click="quickBindCert">🔒 一键生成自签证书并绑定（→ HTTPS 代理）</n-button>
              <div class="form-tip">用该服务器地址自动签一张证书并绑定，公网使用更安全。客户端需勾选「跳过证书验证」；要浏览器/系统信任则改用 ACME 证书。</div>
            </div>
          </n-form-item>
          <n-form-item v-if="ie.type === 'mixed' && ie.tls_id" label=" ">
            <n-tag type="success" size="small">已绑定 TLS，保存后即为 HTTPS 代理（1Panel 代理类型选 HTTPS）</n-tag>
          </n-form-item>
          <n-form-item v-if="['vless','vmess','trojan'].includes(ie.type) && !ie.tls_id && ie.type !== 'shadowsocks'" label=" ">
            <n-tag type="warning" size="small">未配置 TLS，建议为 VLESS/VMess/Trojan 绑定 TLS 或 Reality</n-tag>
          </n-form-item>
          <n-form-item v-if="ie.type === 'vless' && ie.tls_id" label="Flow">
            <n-select v-model:value="ie.flow" :options="[
              {label:'xtls-rprx-vision（推荐）',value:'xtls-rprx-vision'},
              {label:'关闭',value:'none'}
            ]" />
          </n-form-item>
          <n-form-item label="TCP Fast Open"><n-switch v-model:value="ie.tfo" /></n-form-item>
          <n-form-item label="MPTCP"><n-switch v-model:value="ie.mptcp" /></n-form-item>
          <n-form-item label="启用"><n-switch v-model:value="ie.enabled" /></n-form-item>
          <template v-if="ie.type === 'tuic'">
            <n-form-item label="拥塞控制"><n-select v-model:value="ie.cc" :options="[{label:'bbr',value:'bbr'},{label:'cubic',value:'cubic'},{label:'new_reno',value:'new_reno'}]" /></n-form-item>
            <n-form-item label="0-RTT"><n-switch v-model:value="ie.zero_rtt" /></n-form-item>
          </template>
          <template v-if="ie.type === 'hysteria2' || ie.type === 'hysteria'">
            <n-form-item label="上行 Mbps"><n-input-number v-model:value="ie.up_mbps" :min="0" style="width:100%;" /></n-form-item>
            <n-form-item label="下行 Mbps"><n-input-number v-model:value="ie.down_mbps" :min="0" style="width:100%;" /></n-form-item>
          </template>
          <template v-if="ie.type === 'hysteria2'">
            <n-form-item label="混淆类型">
              <n-select v-model:value="ie.obfs_type" :options="[
                {label:'关闭',value:''},
                {label:'salamander',value:'salamander'},
                {label:'gecko（1.14+）',value:'gecko'}
              ]" />
            </n-form-item>
            <n-form-item v-if="ie.obfs_type" label="混淆密码"><n-input v-model:value="ie.obfs_password" placeholder="必填" /></n-form-item>
            <template v-if="ie.obfs_type === 'gecko'">
              <n-form-item label="gecko 最小包">
                <n-input-number v-model:value="ie.obfs_min_packet" :min="0" placeholder="默认 512" style="width:100%;" />
              </n-form-item>
              <n-form-item label="gecko 最大包">
                <n-input-number v-model:value="ie.obfs_max_packet" :min="0" placeholder="默认 1200" style="width:100%;" />
              </n-form-item>
            </template>
            <n-form-item label="BBR 配置">
              <n-select v-model:value="ie.bbr_profile" :options="[
                {label:'默认（standard）',value:''},
                {label:'conservative',value:'conservative'},
                {label:'standard',value:'standard'},
                {label:'aggressive',value:'aggressive'}
              ]" />
            </n-form-item>
            <n-form-item label="关闭 Chrome QUIC 鹦鹉">
              <n-switch v-model:value="ie.disable_chrome_parrot" />
              <template #feedback>默认开启鹦鹉；Ed25519 证书或不兼容客户端可关闭。仅写入订阅出站，不下发入站。</template>
            </n-form-item>
            <n-form-item label="跳港间隔">
              <n-input v-model:value="ie.hop_interval" placeholder="如 30s，留空不写" />
            </n-form-item>
            <n-form-item label="跳港间隔上限">
              <n-input v-model:value="ie.hop_interval_max" placeholder="如 60s，与间隔组成随机区间" />
            </n-form-item>
            <n-form-item label="伪装 URL"><n-input v-model:value="ie.masquerade" placeholder="留空不伪装" /></n-form-item>
            <n-alert type="info" :bordered="false" style="margin-bottom:12px;">
              Hysteria Realm（NAT 穿透）本版暂不提供表单；需要时请在自定义配置里写 realm 块，或等后续跟进。节点需 sing-box ≥ 1.14 才能使用 gecko / bbr_profile / parrot / hop。跳港字段为客户端提示，需配合 server_ports 才生效。
            </n-alert>
          </template>
          <template v-if="ie.type === 'shadowsocks'">
            <n-form-item label="加密方式"><n-select v-model:value="ie.ss_method" :options="ssOpts" /></n-form-item>
          </template>
          <template v-if="ie.type === 'anytls'">
            <n-form-item label="空闲检查(秒)"><n-input-number v-model:value="ie.anytls_idle_check" :min="0" placeholder="30" style="width:100%;" /></n-form-item>
            <n-form-item label="空闲超时(秒)"><n-input-number v-model:value="ie.anytls_idle_timeout" :min="0" placeholder="30" style="width:100%;" /></n-form-item>
            <n-form-item label="最小空闲会话"><n-input-number v-model:value="ie.anytls_min_idle" :min="0" placeholder="0" style="width:100%;" /></n-form-item>
          </template>
          <template v-if="['vless','vmess','trojan'].includes(ie.type)">
            <n-form-item label="传输层"><n-select v-model:value="ie.net" :options="[{label:'TCP',value:'tcp'},{label:'WebSocket',value:'ws'},{label:'gRPC',value:'grpc'},{label:'HTTPUpgrade',value:'httpupgrade'}]" /></n-form-item>
            <n-form-item v-if="ie.net === 'ws' || ie.net === 'httpupgrade'" label="路径"><n-input v-model:value="ie.ws_path" placeholder="/" /></n-form-item>
            <n-form-item v-if="ie.net === 'ws' || ie.net === 'httpupgrade'" label="Host 头"><n-input v-model:value="ie.ws_host" placeholder="留空=同 SNI" /></n-form-item>
            <n-form-item v-if="ie.net === 'ws' || ie.net === 'httpupgrade'" label="Early Data"><n-input-number v-model:value="ie.ws_early_data" :min="0" placeholder="0=关闭" style="width:100%;" /></n-form-item>
            <n-form-item v-if="ie.net === 'grpc'" label="服务名"><n-input v-model:value="ie.grpc_service" placeholder="grpc-service" /></n-form-item>
            <n-form-item v-if="ie.net === 'grpc'" label="Multi Mode"><n-switch v-model:value="ie.grpc_multi" /></n-form-item>
            <n-form-item label="多路复用"><n-switch v-model:value="ie.mux" /></n-form-item>
            <template v-if="ie.mux">
              <n-form-item label="Brutal"><n-switch v-model:value="ie.brutal" /></n-form-item>
              <n-form-item v-if="ie.brutal" label="Brutal 上行"><n-input-number v-model:value="ie.brutal_up" :min="0" style="width:100%;" /></n-form-item>
              <n-form-item v-if="ie.brutal" label="Brutal 下行"><n-input-number v-model:value="ie.brutal_down" :min="0" style="width:100%;" /></n-form-item>
            </template>
          </template>
        </n-form>
        <n-button type="primary" block :loading="saving" @click="saveInbound">保存</n-button>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import {
  NTabs, NTabPane, NDrawer, NDrawerContent, NButton, NForm, NFormItem, NInput, NInputNumber, NInputGroup,
  NSelect, NSwitch, NRadioGroup, NRadio, NTag, NSpin, NEmpty, NCode, NCheckbox, NCollapse, NCollapseItem,
  NAlert, NPopselect, useMessage, useDialog
} from 'naive-ui'
import { apiList, apiGet, apiGetRaw, apiPost, apiPut, apiDelete } from '@/api'

const message = useMessage()
const dialog = useDialog()
const tab = ref('tls')
const loading = ref(false)
const saving = ref(false)
const quickCertLoading = ref(false)
// 整页对 IP 打码，默认开启：这一页几乎每个视图都会把机器/出口的真实地址摆出来，
// 截图发群里就漏了。选择记在 localStorage，免得每次进来都要再点一次。
const IP_KEY = 'qz.sb.showIp'
const showIp = ref(localStorage.getItem(IP_KEY) === '1')
function toggleIp() {
  showIp.value = !showIp.value
  try { localStorage.setItem(IP_KEY, showIp.value ? '1' : '0') } catch {}
}
// 长下拉统一配置：选项名普遍很长（「入站 · 协议 @ 机器」「名称 · 类型 地址:端口」），
// 单行省略号一截就分不清谁是谁。菜单不跟随触发器宽度 + 选项换行 + 可搜索；
// 换行与虚拟滚动的固定行高冲突（行是按 index×行高 定位的，换行会压到下一行上），
// 所以换行时必须关掉虚拟滚动。
const longSel = {
  filterable: true,
  virtualScroll: false,
  consistentMenuWidth: false,
  menuProps: { class: 'qz-wide-menu' },
}
// 但列表可以很大——出口「粘贴导入」一次就允许 500 条。到那个量级，一次性渲染
// 全部选项的代价比省略号更疼，于是保留虚拟滚动、放弃换行，靠「菜单按内容宽度」
// 兜住长名字。给绑定动态列表的下拉用。
const VIRTUAL_FROM = 120
function longSelFor(n: number) {
  return n > VIRTUAL_FROM ? { filterable: true, consistentMenuWidth: false } : longSel
}
const genLoading = ref(false)

// 抽屉宽度：移动端全屏，桌面 500px
const isMobile = ref(false)
const drawerW = computed(() => isMobile.value ? '100%' : 500)
function checkMobile() { isMobile.value = window.matchMedia('(max-width: 768px)').matches }
onMounted(() => { checkMobile(); window.addEventListener('resize', checkMobile); load(); refreshSyncStatus() })
onUnmounted(() => { window.removeEventListener('resize', checkMobile); if (syncTimer) clearInterval(syncTimer) })

// ===== 配置同步状态：入站保存后是异步推送到各机器，这里回显每台机的下发结果 =====
const syncStatus = ref<Record<string, any>>({})
let syncTimer: any = null
async function refreshSyncStatus() {
  try { syncStatus.value = (await apiGet<Record<string, any>>('/api/admin/sb/sync-status')) || {} } catch {}
}
// 保存/挂载/解除后短暂轮询，直到所有目标不再处于 pending/running。
function pollSyncStatus() {
  if (syncTimer) clearInterval(syncTimer)
  let ticks = 0
  syncTimer = setInterval(async () => {
    await refreshSyncStatus()
    ticks++
    const busy = Object.values(syncStatus.value).some((s: any) => s && (s.state === 'pending' || s.state === 'running'))
    if (!busy || ticks >= 20) { clearInterval(syncTimer); syncTimer = null }
  }, 1500)
}
// 某台机器（含本机 id=0）的下发状态徽标。
type SyncBadge = { type: 'default' | 'success' | 'warning' | 'error' | 'info' | 'primary'; text: string; title: string }
function syncBadge(machineId: number): SyncBadge | null {
  const s = syncStatus.value[String(machineId)]
  if (!s || !s.state) return null
  switch (s.state) {
    case 'pending': return { type: 'default', text: '待下发', title: '排队等待推送配置' }
    case 'running': return { type: 'warning', text: '下发中…', title: '正在推送配置到该机器' }
    case 'ok': return { type: 'success', text: '已同步', title: '配置已成功下发' }
    case 'failed': {
      const tripped = String(s.error || '').includes('熔断')
      return { type: 'error', text: tripped ? '已熔断' : '下发失败', title: s.error || '推送失败，将在下个周期自动重试' }
    }
    default: return null
  }
}
// 失败原因原文。返回 null 表示这台机器没失败（'' 表示失败但后端没记下原因）。
function syncError(machineId: number): string | null {
  const s = syncStatus.value[String(machineId)]
  if (!s || s.state !== 'failed') return null
  return s.error || ''
}
function syncAt(machineId: number): number {
  const s = syncStatus.value[String(machineId)]
  return s?.at || 0
}
function agoText(ts: number): string {
  if (!ts) return ''
  const d = Math.max(0, Math.floor(Date.now() / 1000) - ts)
  if (d < 60) return `${d} 秒前`
  if (d < 3600) return `${Math.floor(d / 60)} 分钟前`
  if (d < 86400) return `${Math.floor(d / 3600)} 小时前`
  return `${Math.floor(d / 86400)} 天前`
}
// 重新下发：排队一次该机器的配置推送。后端立即返回（SSH 推送最长 90s，HTTP
// 写超时 30s，同步等会把「其实成功了」报成失败），结果靠轮询 sync-status 回显。
const resyncing = ref<number | null>(null)
async function resync(machineId: number) {
  resyncing.value = machineId
  try {
    await apiPost('/api/admin/sb/resync', { server_id: machineId })
    message.success('已排队重新下发，稍候看这台机器的状态')
    pollSyncStatus()
  } catch (e: any) { message.error(e.message || '重新下发失败') } finally { resyncing.value = null }
}

const tlsList = ref<any[]>([])
const certList = ref<any[]>([])
const inbounds = ref<any[]>([])
const servers = ref<any[]>([])
const egresses = ref<any[]>([])
const enabledInboundCount = computed(() => inbounds.value.filter(item => item.enabled).length)
const egressBoundCount = computed(() => inbounds.value.filter(item => item.egress_id).length)
const syncHealthyCount = computed(() => machines.value.filter(machine => syncStatus.value[String(machine.id)]?.state === 'ok').length)
const syncFailedCount = computed(() => machines.value.filter(machine => syncStatus.value[String(machine.id)]?.state === 'failed').length)
const previewJson = ref('')
const previewLoading = ref(false)
const previewSid = ref<number | null>(null)
// True when a preview rendered successfully but has zero inbounds — the usual
// cause of a "空的" preview is having the wrong machine selected.
// 预览区显示用文本：隐藏 IP 时按打码渲染，复制/校验仍走 previewJson 原文。
const previewShown = computed(() => (showIp.value ? previewJson.value : maskConfigText(previewJson.value)))
const previewNoInbounds = computed(() => {
  if (!previewJson.value) return false
  try { return (JSON.parse(previewJson.value).inbounds || []).length === 0 } catch { return false }
})

// 搜索/筛选
const tlsSearch = ref('')
const inbSearch = ref('')
const filteredTls = computed(() => {
  const q = tlsSearch.value.trim().toLowerCase()
  if (!q) return tlsList.value
  return tlsList.value.filter(t => (t.name || '').toLowerCase().includes(q) || (jp(t.server_json).server_name || '').toLowerCase().includes(q))
})
const filteredInbounds = computed(() => {
  const q = inbSearch.value.trim().toLowerCase()
  if (!q) return inbounds.value
  return inbounds.value.filter(n => (n.tag || '').toLowerCase().includes(q) || (n.type || '').toLowerCase().includes(q))
})

// ========== 按机器分组（一台机器一张卡片） ==========
// 机器 = 本机(server_id 0) + 每台远程服务器。
const machines = computed(() => {
  const list = [{ id: 0, name: '本机', host: '面板本机', enabled: true, isLocal: true }]
  for (const s of servers.value) {
    if (s.enabled === false) continue // 已禁用的机器不在此显示（可在「服务器」页启用后回来管理）
    list.push({ id: s.id, name: s.name || ('服务器 #' + s.id), host: s.host || '—', enabled: true, isLocal: false })
  }
  return list
})

// 每台机器一组，携带该机器的入站（经搜索过滤）与启用/总数统计。
// 搜索状态下隐藏没有命中的机器，避免噪音。
const inboundGroups = computed(() => {
  const searching = !!inbSearch.value.trim()
  const matched = filteredInbounds.value
  return machines.value.map(m => {
    const all = inbounds.value.filter(n => (n.server_id || 0) === m.id)
    const items = matched.filter(n => (n.server_id || 0) === m.id)
    return { machine: m, items, total: all.length, enabledCount: all.filter(n => n.enabled).length }
  }).filter(g => !searching || g.items.length > 0)
})

// TLS 配置按机器分组显示（与入站页一致）。搜索时只保留有匹配项的机器。
const tlsGroups = computed(() => {
  const searching = !!tlsSearch.value.trim()
  const matched = filteredTls.value
  return machines.value.map(m => {
    const all = tlsList.value.filter(t => (t.server_id || 0) === m.id)
    const items = matched.filter(t => (t.server_id || 0) === m.id)
    return { machine: m, items, total: all.length }
  }).filter(g => !searching || g.items.length > 0)
})

// 折叠状态：默认全部展开；首次加载后按机器 id 铺开。
const expandedMachines = ref<number[]>([])
let expandedInit = false
const allExpanded = computed(() => expandedMachines.value.length >= machines.value.length)
function toggleAllMachines() {
  expandedMachines.value = allExpanded.value ? [] : machines.value.map(m => m.id)
}

// ===== 列表排序 =====
// TLS 与入站都按 sort_order 全局排，页面只是按机器分组展示，所以「上移/下移」＝
// 在全局数组里和同组的相邻项对调，再把完整 id 顺序提交给后端。乐观更新，失败回滚重载。
const reordering = ref(false)
async function moveWithin(list: any[], setList: (v: any[]) => void, groupItems: any[], idx: number, dir: -1 | 1, url: string) {
  const target = idx + dir
  if (target < 0 || target >= groupItems.length || reordering.value) return
  const aId = groupItems[idx].id
  const bId = groupItems[target].id
  const arr = [...list]
  const i = arr.findIndex(x => x.id === aId)
  const j = arr.findIndex(x => x.id === bId)
  if (i < 0 || j < 0) return
  ;[arr[i], arr[j]] = [arr[j], arr[i]]
  setList(arr)
  reordering.value = true
  try {
    await apiPost(url, { ids: arr.map(x => x.id) })
  } catch (e: any) {
    message.error(e.message || '排序失败'); await load()
  } finally { reordering.value = false }
}
// 搜索状态下组内看到的是过滤后的子集，相邻两项在全局里未必相邻——照样只交换这两项，
// 结果与所见一致。
function moveTls(g: any, idx: number, dir: -1 | 1) {
  return moveWithin(tlsList.value, v => (tlsList.value = v), g.items, idx, dir, '/api/admin/sb/tls/reorder')
}
function moveInbound(g: any, idx: number, dir: -1 | 1) {
  return moveWithin(inbounds.value, v => (inbounds.value = v), g.items, idx, dir, '/api/admin/sb/inbounds/reorder')
}

// 在指定机器下新增入站（预填所属服务器）。
function openInboundFor(serverId: number) {
  resetIe()
  chainSourceId.value = 0
  ie.server_id = serverId
  showInb.value = true
}

// 新建 TLS 并预选所属机器（TLS 页按机器分组时的「＋ TLS」入口）。
function openTlsFor(serverId: number) {
  openTls()
  te.server_id = serverId
}

// 跳到「配置预览」并选中该机器。
function previewMachine(id: number) {
  tab.value = 'preview'
  previewSid.value = id || null
  previewPicked = true // explicit choice — don't let a later tab revisit override it
  loadPreview()
}

// 首次进入「配置预览」时，默认选中一台真正有入站的机器（入站通常建在服务器上，
// 而非本机），并自动加载，避免默认停在空的本机预览上。
let previewPicked = false
function pickPreviewDefault() {
  if (previewPicked) return
  const withEnabled = machines.value.find(m => inbounds.value.some(n => (n.server_id || 0) === m.id && n.enabled))
  const withAny = machines.value.find(m => inbounds.value.some(n => (n.server_id || 0) === m.id))
  const target = withEnabled || withAny
  if (target) previewSid.value = target.id || null
  previewPicked = true
}
function onTabChange(name: string) {
  if (name === 'preview') { pickPreviewDefault(); loadPreview() }
}

// 批量操作
const checkedIds = ref(new Set<number>())
function toggleCheck(id: number) {
  const s = new Set(checkedIds.value)
  if (s.has(id)) s.delete(id); else s.add(id)
  checkedIds.value = s
}

// 端口测试
const portChecking = ref<number | null>(null)
const portResult = ref<Record<number, any>>({})

function jp(s: string) { try { return JSON.parse(s || '{}') } catch { return {} } }

const serverOpts = computed(() => [{ label: '本机', value: 0 }, ...servers.value.map(s => ({ label: s.name, value: s.id }))])

// Relay chaining: which inbounds can serve as a landing (must be renderable as a
// sing-box outbound). anytls / hysteria v1 have no outbound renderer, so they
// can't be a relay's upstream.
const RELAY_LANDING_TYPES = ['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'tuic']
const inboundById = computed(() => { const m: Record<number, any> = {}; for (const n of inbounds.value) m[n.id] = n; return m })
function landingOf(r: any) { return r && r.upstream_inbound_id ? (inboundById.value[r.upstream_inbound_id] || null) : null }
// 从某入站出发沿 upstream 链走到头，返回拓扑段列表（多级中转 + 末端出口），防环。
function chainOf(r: any) {
  const segs: any[] = []
  const seen = new Set<number>([r.id])
  let cur = r
  while (cur.upstream_inbound_id) {
    const next = inboundById.value[cur.upstream_inbound_id]
    if (!next || seen.has(next.id)) { segs.push({ kind: 'broken-landing' }); return segs }
    seen.add(next.id)
    segs.push({ kind: 'landing', ib: next })
    cur = next
  }
  if (cur.egress_id) {
    const e = egressById.value[cur.egress_id]
    segs.push(e ? { kind: 'egress', eg: e } : { kind: 'broken-egress' })
  }
  return segs
}
// 判断沿 start 的 upstream 链是否会到达 target（选择落地时用来排除会成环的选项）。
function chainReaches(startId: number, targetId: number): boolean {
  const seen = new Set<number>()
  let cur = inboundById.value[startId]
  while (cur) {
    if (cur.id === targetId) return true
    if (seen.has(cur.id)) return false
    seen.add(cur.id)
    cur = cur.upstream_inbound_id ? inboundById.value[cur.upstream_inbound_id] : null
  }
  return false
}
const landingOpts = computed(() => [
  { label: '直接出网（本入站即落地）', value: 0 },
  ...inbounds.value
    .filter(n => n.id !== ie.id && n.enabled && RELAY_LANDING_TYPES.includes(n.type) && !(ie.id && chainReaches(n.id, ie.id)))
    .map(n => ({
      label: `${n.tag} · ${(n.type || '').toUpperCase()} @ ${serverName(n.server_id)}`
        + (n.upstream_inbound_id || n.egress_id ? '（继续转发）' : ''),
      value: n.id,
    })),
])
// value 0 = 未绑定；显式给个「无」选项，否则 n-select 会把裸值 0 显示出来。
const tlsOpts = computed(() => [{ label: '无', value: 0 }, ...tlsList.value.map(t => ({ label: t.name + ' (' + t.mode + ')', value: t.id }))])
const protoOpts = [
  { label: 'VLESS', value: 'vless' }, { label: 'VMess', value: 'vmess' }, { label: 'Trojan', value: 'trojan' },
  { label: 'TUIC', value: 'tuic' }, { label: 'Hysteria2', value: 'hysteria2' }, { label: 'Shadowsocks', value: 'shadowsocks' },
  { label: 'AnyTLS', value: 'anytls' }, { label: 'Hysteria v1', value: 'hysteria' },
  { label: 'Mixed (HTTP/SOCKS5 代理)', value: 'mixed' },
]
const fpOpts = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized'].map(v => ({ label: v, value: v }))
const verOpts = [{ label: '1.2', value: '1.2' }, { label: '1.3', value: '1.3' }]
const ssOpts = ['2022-blake3-aes-128-gcm', '2022-blake3-aes-256-gcm', '2022-blake3-chacha20-poly1305'].map(v => ({ label: v, value: v }))
const listenOpts = [
  { label: '::（IPv6+IPv4，默认）', value: '::' },
  { label: '0.0.0.0（仅 IPv4）', value: '0.0.0.0' },
  { label: '127.0.0.1（仅本机）', value: '127.0.0.1' },
]
const sniPresets = [
  'www.microsoft.com', 'www.tesla.com', 'www.apple.com', 'www.icloud.com',
  'www.akamai.com', 'www.cloudflare.com', 'www.amazon.com', 'gateway.icloud.com',
].map(v => ({ label: v, value: v }))
const presetOpts = [
  { label: 'VLESS + Reality + Vision', value: 'vless-reality' },
  { label: 'VLESS + WS + TLS', value: 'vless-ws-tls' },
  { label: 'Hysteria2', value: 'hysteria2' },
  { label: 'TUIC', value: 'tuic' },
  { label: 'Trojan + TLS', value: 'trojan-tls' },
  { label: 'Shadowsocks 2022', value: 'shadowsocks' },
  { label: 'Mixed (HTTP/SOCKS5 代理)', value: 'mixed' },
]

// maskHost 对 IP/域名打码，链路拓扑默认用它，防止截图/分享时泄露真实地址。
// IPv4 保留首尾段（38.***.***.54）；域名/IPv6 保留首尾各两位；非地址标签（如「面板本机」）原样返回。
function maskHost(h: string): string {
  if (!h) return h
  const v4 = h.match(/^(\d{1,3})\.\d{1,3}\.\d{1,3}\.(\d{1,3})$/)
  if (v4) return `${v4[1]}.***.***.${v4[2]}`
  if (!/[.:]/.test(h)) return h // 「面板本机」「—」等非地址标签
  if (h.length <= 6) return '***'
  return h.slice(0, 2) + '***' + h.slice(-2)
}
// 页面里所有展示地址的地方都走这里，由右上角开关统一决定明文还是打码。
function dispHost(h: string): string { return showIp.value ? h : maskHost(h) }

// 私有/回环/链路本地地址不打码：它们本来就不泄露任何东西，打了反而让
// 「listen: 127.0.0.1」这类配置读不懂。
function isPrivateV4(ip: string): boolean {
  const p = ip.split('.').map(Number)
  if (p.length !== 4 || p.some(n => !(n >= 0 && n <= 255))) return true // 不是合法 IP，别动
  if (p[0] === 0 || p[0] === 10 || p[0] === 127) return true
  if (p[0] === 192 && p[1] === 168) return true
  if (p[0] === 172 && p[1] >= 16 && p[1] <= 31) return true
  if (p[0] === 169 && p[1] === 254) return true
  if (p[0] === 100 && p[1] >= 64 && p[1] <= 127) return true // CGNAT
  if (p[0] >= 224) return true                               // 组播/广播
  return false
}
// 给配置预览打码：先按已知主机（机器、代理出口，含域名）整串替换，再兜底扫一遍
// 公网 IPv4 字面量。只影响显示，「复制配置」拿的仍是 previewJson 原文。
function maskConfigText(s: string): string {
  if (!s) return s
  const hosts = new Set<string>()
  for (const m of machines.value) if (m.host && /[.:]/.test(m.host)) hosts.add(m.host)
  for (const e of egresses.value) if (e.host) hosts.add(e.host)
  let out = s
  // 长的先替换，否则 "a.example.com" 会被其后缀 "example.com" 先切掉一半。
  for (const h of [...hosts].sort((a, b) => b.length - a.length)) out = out.split(h).join(maskHost(h))
  return out.replace(/\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b/g, ip => (isPrivateV4(ip) ? ip : maskHost(ip)))
}
// 后端返回的自由文本（下发失败原因、出口测试输出）同样会带上地址——
// 「ssh connect 179.253.224.226: ...」照抄出来，打码开关就等于没开。
function dispText(s: string): string { return showIp.value ? s : maskConfigText(s || '') }
function serverName(id: number) { if (!id) return '本机'; const s = servers.value.find(s => s.id === id); return s ? s.name : '#' + id }
function tlsName(id: number) { if (!id) return '无'; const t = tlsList.value.find(t => t.id === id); return t ? t.name : '#' + id }
function tlsUseCount(id: number) { return inbounds.value.filter(n => n.tls_id === id).length }

// ========== TLS ==========
const showTls = ref(false)
const te = reactive({
  id: 0, mode: 'reality', name: '', server_id: 0, server_name: 'www.microsoft.com',
  handshake_server: '', handshake_port: 443, fingerprint: 'chrome',
  private_key: '', public_key: '', short_ids: [] as string[],
  certificate: '', key: '', alpn: [] as string[], min_version: '', max_version: '', insecure: false,
  cert_id: 0,
})

function openTls(t?: any, clone = false) {
  sniResult.value = null
  hsResult.value = null
  acme.method = 'dns-cf'; acme.cf_token = ''; acme.webroot = ''; acme.email = ''
  if (t) {
    const s = jp(t.server_json), c = jp(t.client_json), r = s.reality || {}, hs = r.handshake || {}
    // short_id 可能是数组或单值，统一为数组
    let sids: string[] = []
    if (Array.isArray(r.short_id)) sids = r.short_id.filter((x: string) => x)
    else if (r.short_id) sids = [r.short_id]
    if (c.short_id && !sids.includes(c.short_id)) sids.unshift(c.short_id)
    Object.assign(te, {
      id: clone ? 0 : t.id, mode: t.mode, name: clone ? t.name + ' (副本)' : t.name, server_id: t.server_id || 0,
      server_name: s.server_name || '', handshake_server: hs.server || '', handshake_port: hs.server_port || 443,
      fingerprint: (c.utls && c.utls.fingerprint) || 'chrome',
      private_key: r.private_key || '', public_key: (c.reality && c.reality.public_key) || '',
      short_ids: sids.length ? sids : [''],
      certificate: Array.isArray(s.certificate) ? s.certificate.join('\n') : (s.certificate || ''), key: Array.isArray(s.key) ? s.key.join('\n') : (s.key || ''),
      alpn: s.alpn || [], min_version: s.min_version || '', max_version: s.max_version || '',
      insecure: !!c.insecure, cert_id: t.cert_id || 0,
    })
  } else {
    Object.assign(te, {
      id: 0, mode: 'reality', name: '', server_id: 0, server_name: 'www.microsoft.com',
      handshake_server: '', handshake_port: 443, fingerprint: 'chrome',
      private_key: '', public_key: '', short_ids: [''],
      certificate: '', key: '', alpn: ['h3', 'h2', 'http/1.1'], min_version: '', max_version: '', insecure: false,
      cert_id: 0,
    })
  }
  showTls.value = true
}

function cloneTls(t: any) { openTls(t, true) }

// 证书中心的证书可被 TLS 配置引用（cert_id）：选中后由构建时注入真实 PEM，
// 一张证书续期后所有引用它的入站自动生效。0 = 不引用，手动粘贴/自签。
const certOpts = computed(() => [
  { label: '不引用（手动粘贴 / 自签，见下方）', value: 0 },
  ...certList.value.map(c => ({ label: `${c.name}（${c.domain || '无域名'}·${c.source === 'acme' ? '真实证书' : c.source === 'paste' ? '导入' : '自签'}）`, value: c.id })),
])
function onCertPick(id: number) {
  te.cert_id = id
  const c = certList.value.find(x => x.id === id)
  if (c && c.domain) te.server_name = c.domain // SNI 必须与证书域名一致
}

// --- SNI 延迟测试 ---
const sniTesting = ref(false)
const sniResult = ref<any>(null)
async function testSni() {
  const host = (te.server_name || '').trim()
  if (!host) { message.warning('请输入 SNI 域名'); return }
  sniTesting.value = true
  sniResult.value = null
  try {
    sniResult.value = await apiGet<any>(`/api/admin/sb/sni-test?host=${encodeURIComponent(host)}`)
  } catch (e: any) {
    message.error(e.message || '测试失败')
  } finally {
    sniTesting.value = false
  }
}

// --- Reality 握手目标延迟测试 ---
const hsTesting = ref(false)
const hsResult = ref<any>(null)
async function testHandshake() {
  const host = (te.handshake_server || te.server_name || '').trim()
  if (!host) { message.warning('请输入握手目标'); return }
  const port = te.handshake_port || 443
  hsTesting.value = true
  hsResult.value = null
  try {
    hsResult.value = await apiGet<any>(`/api/admin/sb/sni-test?host=${encodeURIComponent(host)}&port=${port}`)
  } catch (e: any) {
    message.error(e.message || '测试失败')
  } finally {
    hsTesting.value = false
  }
}

// ACME 在线申请：调用后端 acme.sh 签发真实证书（仅本机），成功后直接落库并刷新。
const acme = reactive({ method: 'dns-cf', cf_token: '', webroot: '', email: '' })
const acmeLoading = ref(false)
async function requestAcme() {
  const domain = (te.server_name || '').trim()
  if (!te.name || !domain) { message.error('名称和 SNI（域名）必填'); return }
  if (acme.method === 'dns-cf' && !acme.cf_token.trim()) { message.error('Cloudflare DNS 需填 API Token'); return }
  if (acme.method === 'webroot' && !acme.webroot.trim()) { message.error('Webroot 方式需填网站根目录'); return }
  acmeLoading.value = true
  try {
    await apiPost('/api/admin/sb/tls/acme', {
      name: te.name, server_id: te.server_id || 0, server_name: domain,
      method: acme.method, cf_token: acme.cf_token.trim(), webroot: acme.webroot.trim(), email: acme.email.trim(),
    })
    message.success('证书申请成功，已保存')
    showTls.value = false
    await load()
  } catch (e: any) { message.error(e.message || '申请失败') } finally { acmeLoading.value = false }
}

// 一键生成自签证书：按当前 SNI 生成 PEM 证书+私钥并填入表单。
const genCertLoading = ref(false)
async function genSelfSigned() {
  const host = (te.server_name || '').trim()
  if (!host) { message.warning('请先填写 SNI 域名'); return }
  genCertLoading.value = true
  try {
    const r = await apiPost<any>('/api/admin/sb/tls/self-signed', { server_name: host })
    te.certificate = r?.certificate || ''
    te.key = r?.key || ''
    message.success('自签证书已生成，请检查后保存')
  } catch (e: any) { message.error(e.message || '生成失败') } finally { genCertLoading.value = false }
}

async function genKeys() {
  genLoading.value = true
  try {
    const r = await apiPost<any>('/api/admin/sb/reality-keypair')
    te.private_key = r?.private_key || ''
    te.public_key = r?.public_key || ''
    // 生成时填入第一个 short_id 槽位
    if (te.short_ids.length === 0) te.short_ids.push('')
    te.short_ids[0] = r?.short_id || ''
    message.success('密钥对已生成')
  } catch (e: any) { message.error(e.message) } finally { genLoading.value = false }
}

async function saveTls() {
  if (!te.name || !te.server_name) { message.error('名称和 SNI 必填'); return }
  saving.value = true
  try {
    const ep = te.mode === 'reality' ? '/api/admin/sb/tls/reality' : '/api/admin/sb/tls/cert'
    const url = te.id ? ep + '/' + te.id : ep
    const fn = te.id ? apiPut : apiPost
    const body = te.mode === 'reality'
      ? { name: te.name, server_id: te.server_id, server_name: te.server_name, handshake_server: te.handshake_server, handshake_port: te.handshake_port, fingerprint: te.fingerprint, private_key: te.private_key, public_key: te.public_key, short_ids: te.short_ids.filter(s => s.trim()) }
      : { name: te.name, server_id: te.server_id, server_name: te.server_name, cert_id: te.cert_id || 0, certificate: te.certificate, key: te.key, insecure: te.insecure, alpn: te.alpn, fingerprint: te.fingerprint, min_version: te.min_version, max_version: te.max_version }
    await fn(url, body)
    message.success('保存成功'); showTls.value = false; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

async function deleteTls(id: number) { try { await apiDelete('/api/admin/sb/tls/' + id); message.success('已删除'); await load() } catch (e: any) { message.error(e.message) } }

// ========== Egress（第三方代理出口，如购买的静态 IP SOCKS5/HTTP） ==========
const showEg = ref(false)
const egBlank = {
  id: 0, name: '', type: 'socks', host: '', port: 1080, username: '', password: '',
  tls_enabled: false, sni: '', tls_cert_id: 0, tls_insecure: false,
  udp_mode: '', connect_timeout_ms: 0,
}
const ee = reactive({ ...egBlank })
function openEgress(r?: any) {
  Object.assign(ee, r
    ? {
        id: r.id, name: r.name, type: r.type, host: r.host, port: r.port,
        username: r.username || '', password: r.password || '',
        tls_enabled: !!r.tls_enabled, sni: r.sni || '', tls_cert_id: r.tls_cert_id || 0, tls_insecure: !!r.tls_insecure,
        udp_mode: r.udp_mode || '', connect_timeout_ms: r.connect_timeout_ms || 0,
      }
    : { ...egBlank })
  egParseText.value = ''
  showEg.value = true
}
// 与后端 SbEgress.EffectiveUDPMode 同一套规则：留空＝按类型定。表单里照着算一遍，
// 是为了让「自动」这一档也能显示出实际会生成什么，而不是留一个看不出结果的空档。
const effectiveUdpMode = computed(() => {
  if (ee.udp_mode === 'passthrough' || ee.udp_mode === 'block') return ee.udp_mode
  return ee.type === 'http' ? 'block' : 'passthrough'
})
// sing-box 的 SOCKS5 出站没有 TLS 选项，切到 SOCKS5 时把开关一并关掉，
// 否则表单看着是加密的、后端却会拒绝保存。
function onEgressType(t: string) {
  ee.type = t
  if (t !== 'http') ee.tls_enabled = false
  // HTTP 出站没有 UDP 通路，后端会拒绝 http + 透传；切过去时把这个选择收回「自动」，
  // 免得点保存才报错。
  if (t === 'http' && ee.udp_mode === 'passthrough') ee.udp_mode = ''
}

// ---- 粘贴链接解析 ----
const egParseText = ref('')
const egParsing = ref(false)
// 单行解析→回填当前抽屉。名称只在为空时覆盖：编辑一条已有出口时换了地址，
// 不该把管理员起的名字也一起冲掉。
async function parseIntoForm() {
  const text = egParseText.value.trim()
  if (!text) return
  egParsing.value = true
  try {
    const res: any = await apiPost('/api/admin/sb/egresses/parse', { text })
    const it = (res.items || [])[0]
    if (!it) throw new Error((res.errors || [])[0]?.error || '无法解析')
    ee.type = it.type; ee.host = it.host; ee.port = it.port
    ee.username = it.username || ''; ee.password = it.password || ''
    ee.tls_enabled = !!it.tls_enabled; ee.sni = it.sni || ''
    if (!ee.tls_enabled) { ee.tls_cert_id = 0; ee.tls_insecure = false }
    if (ee.type === 'http' && ee.udp_mode === 'passthrough') ee.udp_mode = ''
    if (!ee.name) ee.name = it.name
    egParseText.value = ''
    message.success(it.type_guessed ? `已填充，类型按端口猜为 ${it.type.toUpperCase()}，请核对` : '已填充')
  } catch (e: any) { message.error(e.message) } finally { egParsing.value = false }
}

// ---- 批量粘贴导入 ----
const showEgImport = ref(false)
const egImportText = ref('')
const egImportItems = ref<any[]>([])
const egImportErrors = ref<any[]>([])
const egImportParsing = ref(false)
const egImporting = ref(false)
function openEgressImport() {
  egImportText.value = ''; egImportItems.value = []; egImportErrors.value = []
  showEgImport.value = true
}
async function parseEgressImport() {
  const text = egImportText.value.trim()
  if (!text) { message.warning('请先粘贴内容'); return }
  egImportParsing.value = true
  try {
    const res: any = await apiPost('/api/admin/sb/egresses/parse', { text })
    egImportItems.value = res.items || []
    egImportErrors.value = res.errors || []
    // 超出上限被截掉的部分必须说出来，否则「导入成功」看起来像全都进去了。
    if (res.truncated > 0) message.warning(`内容过长，只解析了前 500 行，已忽略后面 ${res.truncated} 行`)
    if (!egImportItems.value.length) message.warning('没有解析出可导入的条目')
  } catch (e: any) { message.error(e.message) } finally { egImportParsing.value = false }
}
// 逐条走普通的新建接口，而不是加一个批量写入端点：校验规则只有一份，
// 批量导入和手工添加落到库里的东西必然一致。
async function doEgressImport() {
  egImporting.value = true
  const stillFailing: any[] = []
  let done = 0
  let firstError = ''
  try {
    for (const it of egImportItems.value) {
      try {
        await apiPost('/api/admin/sb/egresses', {
          name: it.name, type: it.type, host: it.host, port: it.port,
          username: it.username || '', password: it.password || '',
          tls_enabled: !!it.tls_enabled, sni: it.sni || '', tls_cert_id: 0, tls_insecure: false,
          udp_mode: '', connect_timeout_ms: 0,
        })
        done++
      } catch (e: any) {
        // 按条目本身收集，不靠名字去匹配错误串——名称可重复，也允许含任意字符。
        stillFailing.push(it)
        if (!firstError) firstError = `${it.name}：${e.message}`
      }
    }
  } finally { egImporting.value = false }
  await load()
  if (stillFailing.length) {
    // 已成功的那些留在库里——回滚只会让管理员重来一遍。失败的留在列表里，改完可直接重试
    // （重试只会再提交这些，不会把已成功的重复创建一遍）。
    egImportItems.value = stillFailing
    message.error(`导入 ${done} 条成功，${stillFailing.length} 条失败：${firstError}`)
  } else {
    message.success(`已导入 ${done} 条`)
    showEgImport.value = false
  }
}
// 出口方向我们是 TLS 客户端：这里选的证书是「信任锚」，用来校验上游代理，
// 面板不会把它发出去，私钥那一半也用不到（sing-box 出站不支持 mTLS）。
// 只有代理是你自己搭的、用的正是证书中心这张证书时才需要选。
const egressTrustOpts = computed(() => [
  { label: '系统根证书（商用代理选这个）', value: 0 },
  ...certList.value.map(c => ({ label: `${c.name}（${c.domain || '无域名'}·${c.source === 'acme' ? '真实证书' : c.source === 'paste' ? '导入' : '自签'}）`, value: c.id })),
])
async function saveEgress() {
  saving.value = true
  try {
    const body = {
      name: ee.name, type: ee.type, host: ee.host, port: ee.port,
      username: ee.username, password: ee.password,
      tls_enabled: ee.tls_enabled, sni: ee.sni, tls_cert_id: ee.tls_cert_id, tls_insecure: ee.tls_insecure,
      udp_mode: ee.udp_mode, connect_timeout_ms: ee.connect_timeout_ms || 0,
    }
    if (ee.id) await apiPut('/api/admin/sb/egresses/' + ee.id, body)
    else await apiPost('/api/admin/sb/egresses', body)
    message.success('保存成功'); showEg.value = false; await load()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}
async function deleteEgress(id: number) { try { await apiDelete('/api/admin/sb/egresses/' + id); message.success('已删除'); await load() } catch (e: any) { message.error(e.message) } }
// 克隆走服务端：列表里的密码是掩码 "***"，前端复制一份再新建会把这三个星号
// 当成密码存进去（后端只在带 id 时才把 "***" 解释成「保持原值」）。
async function cloneEgress(r: any) {
  r._cloning = true
  try {
    const created: any = await apiPost(`/api/admin/sb/egresses/${r.id}/clone`, {})
    await load()
    message.success(`已克隆为「${created.name}」`)
  } catch (e: any) { message.error(e.message) } finally { r._cloning = false }
}
const egressById = computed(() => { const m: Record<number, any> = {}; for (const e of egresses.value) m[e.id] = e; return m })
function egressOf(r: any) { return r && r.egress_id ? (egressById.value[r.egress_id] || null) : null }
function egressName(id: number) { const e = egressById.value[id]; return e ? e.name : '—' }
const egressOpts = computed(() => [
  { label: '不使用（直接出网）', value: 0 },
  ...egresses.value.map(e => ({ label: `${e.name} · ${(e.type || '').toUpperCase()} ${dispHost(e.host)}:${e.port}`, value: e.id })),
])
// 拓扑图「＋ 挂出口」的候选列表（不含「不使用」项）。
const egressPopOpts = computed(() => egresses.value.map(e => ({ label: `${e.name} · ${(e.type || '').toUpperCase()} ${dispHost(e.host)}:${e.port}`, value: e.id })))
// 入站编辑抽屉中当前所选出口的类型／UDP 策略，用于 UDP 告警。
const selectedEgressType = computed(() => { const e = egressById.value[ie.egress_id]; return e ? e.type : '' })
const selectedEgressUdp = computed(() => { const e = egressById.value[ie.egress_id]; return e ? e.udp_mode_effective : '' })

// 测试代理出口连通性：后端在引用该出口的节点机上（匹配 IP 白名单）经代理访问 IP 回显服务。
async function testEgress(r: any) {
  r._testing = true
  try {
    r._test = await apiPost(`/api/admin/sb/egresses/${r.id}/test`, {})
  } catch (e: any) {
    r._test = { ok: false, via_server: '—', output: e.message }
  } finally { r._testing = false }
}

// 并发探测：一次开 16 条连接，看供应商的并发上限在哪。
// 单条测通不代表可用——所有绑定此出口的入站共用供应商那一个账号，
// 打开一个网页就会同时开几十条连接，超出上限的会被代理侧直接掐掉，
// 表现是网页加载一半而 App 一切正常。一条一条地测永远看不到这个。
async function probeEgress(r: any) {
  r._probing = true
  try {
    r._test = await apiPost(`/api/admin/sb/egresses/${r.id}/test?n=16`, {})
  } catch (e: any) {
    r._test = { ok: false, via_server: '—', output: e.message }
  } finally { r._probing = false }
}

// 从拓扑图直接给某入站挂上代理出口，无需进编辑抽屉。
async function attachEgress(r: any, egId: number) {
  if (!egId) return
  try {
    await apiPut('/api/admin/sb/inbounds/' + r.id, { type: r.type, tag: r.tag, listen: r.listen || '::', listen_port: r.listen_port, tls_id: r.tls_id, server_id: r.server_id, enabled: r.enabled, upstream_inbound_id: 0, egress_id: egId, options: JSON.stringify(jp(r.options)) })
    message.success('已挂出口，配置推送中…'); await load(); pollSyncStatus()
  } catch (e: any) { message.error(e.message) }
}

// 解除某入站的代理出口，恢复直接出网。
async function unlinkEgress(r: any) {
  try {
    await apiPut('/api/admin/sb/inbounds/' + r.id, { type: r.type, tag: r.tag, listen: r.listen || '::', listen_port: r.listen_port, tls_id: r.tls_id, server_id: r.server_id, enabled: r.enabled, upstream_inbound_id: 0, egress_id: 0, options: JSON.stringify(jp(r.options)) })
    message.success('已解除出口，配置推送中…'); await load(); pollSyncStatus()
  } catch (e: any) { message.error(e.message) }
}

// ========== Inbound ==========
const showInb = ref(false)
const presetType = ref<string | null>(null)
// 若 > 0：本次「添加入站」保存后，把该源入站的落地(upstream)指向新建入站，形成中转链路。
const chainSourceId = ref(0)
const chainSourceName = computed(() => { const s = inbounds.value.find(n => n.id === chainSourceId.value); return s ? s.tag : '' })

// 从拓扑图为某入站串联一个落地：打开「添加入站」抽屉，保存后自动把源入站的落地指向新入站。
function addLandingAfter(r: any) {
  resetIe()
  chainSourceId.value = r.id
  ie.tag = (r.tag || 'node') + '-landing'
  showInb.value = true
}

// 解除某入站的中转，恢复直接出网。
async function unlinkRelay(r: any) {
  try {
    await apiPut('/api/admin/sb/inbounds/' + r.id, { type: r.type, tag: r.tag, listen: r.listen || '::', listen_port: r.listen_port, tls_id: r.tls_id, server_id: r.server_id, enabled: r.enabled, upstream_inbound_id: 0, options: JSON.stringify(jp(r.options)) })
    message.success('已解除中转'); await load()
  } catch (e: any) { message.error(e.message) }
}
// 归一化 flow 值：空或 vision 统一成 xtls-rprx-vision，兼容旧数据和 sing-box 1.10+ 新名
function normFlow(v: any): string {
  if (!v || v === 'vision') return 'xtls-rprx-vision'
  return v
}
const ie = reactive({
  id: 0, type: 'vless', tag: '', listen: '::', listen_port: 443, tls_id: 0, server_id: 0, enabled: true,
  tfo: false, mptcp: false, cc: 'bbr', zero_rtt: false,
  up_mbps: 0, down_mbps: 0, obfs_type: '', obfs_password: '', obfs_min_packet: 0, obfs_max_packet: 0, bbr_profile: '', disable_chrome_parrot: false, hop_interval: '', hop_interval_max: '', masquerade: '',
  net: 'tcp', ws_path: '/', ws_host: '', ws_early_data: 0, grpc_service: '', grpc_multi: false,
  ss_method: '2022-blake3-aes-128-gcm', flow: 'xtls-rprx-vision',
  anytls_idle_check: 0, anytls_idle_timeout: 0, anytls_min_idle: 0,
  mux: false, brutal: false, brutal_up: 0, brutal_down: 0, upstream_inbound_id: 0, egress_id: 0,
})

function resetIe() {
  Object.assign(ie, {
    id: 0, type: 'vless', tag: '', listen: '::', listen_port: 443, tls_id: 0, server_id: 0, enabled: true,
    tfo: false, mptcp: false, cc: 'bbr', zero_rtt: false,
    up_mbps: 0, down_mbps: 0, obfs_type: '', obfs_password: '', obfs_min_packet: 0, obfs_max_packet: 0, bbr_profile: '', disable_chrome_parrot: false, hop_interval: '', hop_interval_max: '', masquerade: '',
    net: 'tcp', ws_path: '/', ws_host: '', ws_early_data: 0, grpc_service: '', grpc_multi: false,
    ss_method: '2022-blake3-aes-128-gcm', flow: 'xtls-rprx-vision',
    anytls_idle_check: 0, anytls_idle_timeout: 0, anytls_min_idle: 0,
    mux: false, brutal: false, brutal_up: 0, brutal_down: 0, upstream_inbound_id: 0, egress_id: 0,
  })
}

function openInbound(n?: any, clone = false) {
  chainSourceId.value = 0
  if (n) {
    const o = jp(n.options), tr = o.transport || {}, mx = o.multiplex || {}, br = mx.brutal || {}, obfs = o.obfs || {}
    Object.assign(ie, {
      id: clone ? 0 : n.id, type: n.type, tag: clone ? n.tag + '-copy' : n.tag, listen: n.listen || '::', listen_port: n.listen_port,
      tls_id: n.tls_id || 0, server_id: n.server_id || 0, enabled: clone ? false : n.enabled,
      tfo: !!o.tcp_fast_open, mptcp: !!o.tcp_multi_path, cc: o.congestion_control || 'bbr', zero_rtt: !!o.zero_rtt_handshake,
      up_mbps: o.up_mbps || 0, down_mbps: o.down_mbps || 0,
      obfs_type: obfs.type || (obfs.password ? 'salamander' : ''), obfs_password: obfs.password || '',
      obfs_min_packet: obfs.min_packet_size || 0, obfs_max_packet: obfs.max_packet_size || 0,
      bbr_profile: o.bbr_profile || '',
      disable_chrome_parrot: !!o.disable_chrome_parrot,
      hop_interval: o.hop_interval || '', hop_interval_max: o.hop_interval_max || '',
      masquerade: typeof o.masquerade === 'string' ? o.masquerade : (o.masquerade?.url || ''),
      net: tr.type || 'tcp', ws_path: tr.path || '/', ws_host: tr.host || '', ws_early_data: tr.max_early_data || 0,
      grpc_service: tr.service_name || '', grpc_multi: !!tr.multi_mode,
      ss_method: o.method || '2022-blake3-aes-128-gcm', flow: normFlow(o.flow),
      anytls_idle_check: o.idle_session_check_interval || 0, anytls_idle_timeout: o.idle_session_timeout || 0, anytls_min_idle: o.min_idle_session || 0,
      mux: !!mx.enabled, brutal: !!br.enabled, brutal_up: br.up_mbps || 0, brutal_down: br.down_mbps || 0,
      upstream_inbound_id: n.upstream_inbound_id || 0, egress_id: n.egress_id || 0,
    })
  } else {
    resetIe()
  }
  showInb.value = true
}

function cloneInbound(n: any) { openInbound(n, true) }

// 一键模板
function applyPreset(v: string | null) {
  if (!v) return
  resetIe()
  chainSourceId.value = 0
  const presets: Record<string, any> = {
    'vless-reality': { type: 'vless', tag: 'vless-reality', listen_port: 443, flow: 'xtls-rprx-vision' },
    'vless-ws-tls': { type: 'vless', tag: 'vless-ws', listen_port: 443, net: 'ws', ws_path: '/ws', flow: 'none' },
    'hysteria2': { type: 'hysteria2', tag: 'hy2', listen_port: 8443, up_mbps: 100, down_mbps: 100 },
    'tuic': { type: 'tuic', tag: 'tuic', listen_port: 8443, cc: 'bbr' },
    'trojan-tls': { type: 'trojan', tag: 'trojan', listen_port: 443 },
    'shadowsocks': { type: 'shadowsocks', tag: 'ss', listen_port: 8388 },
    'mixed': { type: 'mixed', tag: 'mixed', listen_port: 7890 },
  }
  Object.assign(ie, presets[v])
  presetType.value = null
  showInb.value = true
}

// 一键：用该服务器地址签一张自签证书、存为 TLS 条目并绑定到当前入站。
async function quickBindCert() {
  quickCertLoading.value = true
  try {
    const tls: any = await apiPost('/api/admin/sb/tls/quick-selfsigned', { server_id: ie.server_id || 0, name: '自签-' + (ie.tag || 'mixed') })
    tlsList.value = await apiList('/api/admin/sb/tls') // 刷新下拉，让新证书可选
    ie.tls_id = tls.id
    message.success('已生成自签证书并绑定，保存后即为 HTTPS 代理')
  } catch (e: any) { message.error(e.message) } finally { quickCertLoading.value = false }
}

async function saveInbound() {
  saving.value = true
  try {
    const o: any = { tcp_fast_open: ie.tfo, tcp_multi_path: ie.mptcp }
    if (ie.type === 'vless') o.flow = ie.flow
    if (ie.type === 'tuic') { o.congestion_control = ie.cc; o.zero_rtt_handshake = ie.zero_rtt }
    if (ie.type === 'hysteria2' || ie.type === 'hysteria') { if (ie.up_mbps) o.up_mbps = ie.up_mbps; if (ie.down_mbps) o.down_mbps = ie.down_mbps }
    if (ie.type === 'hysteria2') {
      if (ie.obfs_type && ie.obfs_password) {
        const obfs: any = { type: ie.obfs_type, password: ie.obfs_password }
        if (ie.obfs_type === 'gecko') {
          if (ie.obfs_min_packet > 0) obfs.min_packet_size = ie.obfs_min_packet
          if (ie.obfs_max_packet > 0) obfs.max_packet_size = ie.obfs_max_packet
        }
        o.obfs = obfs
      }
      if (ie.bbr_profile) o.bbr_profile = ie.bbr_profile
      if (ie.disable_chrome_parrot) o.disable_chrome_parrot = true
      if (ie.hop_interval) o.hop_interval = ie.hop_interval
      if (ie.hop_interval_max) o.hop_interval_max = ie.hop_interval_max
      if (ie.masquerade) o.masquerade = ie.masquerade
    }
    if (ie.type === 'shadowsocks') o.method = ie.ss_method
    if (ie.type === 'anytls') {
      if (ie.anytls_idle_check) o.idle_session_check_interval = ie.anytls_idle_check
      if (ie.anytls_idle_timeout) o.idle_session_timeout = ie.anytls_idle_timeout
      if (ie.anytls_min_idle) o.min_idle_session = ie.anytls_min_idle
    }
    if (['vless', 'vmess', 'trojan'].includes(ie.type)) {
      if (ie.net !== 'tcp') {
        o.transport = { type: ie.net }
        if (ie.net === 'ws' || ie.net === 'httpupgrade') {
          o.transport.path = ie.ws_path || '/'
          if (ie.ws_host) o.transport.host = ie.ws_host
          if (ie.ws_early_data > 0) { o.transport.max_early_data = ie.ws_early_data; o.transport.early_data_header_name = 'Sec-WebSocket-Protocol' }
        }
        if (ie.net === 'grpc') { o.transport.service_name = ie.grpc_service || ''; o.transport.multi_mode = ie.grpc_multi }
      }
      if (ie.mux) { o.multiplex = { enabled: true }; if (ie.brutal) o.multiplex.brutal = { enabled: true, up_mbps: ie.brutal_up, down_mbps: ie.brutal_down } }
    }
    const body = { type: ie.type, tag: ie.tag, listen: ie.listen || '::', listen_port: ie.listen_port, tls_id: ie.type === 'shadowsocks' ? 0 : (ie.tls_id || 0), server_id: ie.server_id || 0, enabled: ie.enabled, upstream_inbound_id: ie.upstream_inbound_id || 0, egress_id: ie.egress_id || 0, options: JSON.stringify(o) }
    const fn = ie.id ? apiPut : apiPost
    const url = ie.id ? '/api/admin/sb/inbounds/' + ie.id : '/api/admin/sb/inbounds'
    const created = await fn(url, body)
    // 串联落地：把源入站的 upstream 指向刚建的入站，形成 源→新落地 的中转链路。
    if (chainSourceId.value && created && created.id) {
      const src = inbounds.value.find(n => n.id === chainSourceId.value)
      if (src) {
        await apiPut('/api/admin/sb/inbounds/' + src.id, { type: src.type, tag: src.tag, listen: src.listen || '::', listen_port: src.listen_port, tls_id: src.tls_id, server_id: src.server_id, enabled: src.enabled, upstream_inbound_id: created.id, options: JSON.stringify(jp(src.options)) })
      }
    }
    chainSourceId.value = 0
    message.success('已保存，配置推送中…'); showInb.value = false; await load(); pollSyncStatus()
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// 删除一个被中转引用的落地入站，不是一次中性的编辑：那些中转会被解链，
// 流量改从中转机本地出网，出口 IP 静默从落地机变成中转机。所以在删除这一刻
// 就把受影响的中转摆出来，而不是等事后去拓扑页找。
async function deleteInbound(id: number) {
  const referrers = inbounds.value.filter(i => i.upstream_inbound_id === id)
  if (!referrers.length) { await doDeleteInbound(id); return }
  const names = referrers.map(i => `「${i.tag}」`).join('、')
  dialog.error({
    title: '该入站正被中转引用',
    content: `${names} 以此入站为落地。删除后这些中转将改为从各自所在机器直连出网，`
      + '出口 IP 会从当前落地机变成中转机，请确认这是你想要的。'
      + '删除后可在「节点管理 → 链路拓扑」看到它们被标记为已降级。',
    positiveText: '仍然删除', negativeText: '取消',
    onPositiveClick: () => doDeleteInbound(id),
  })
}

async function doDeleteInbound(id: number) {
  try { await apiDelete('/api/admin/sb/inbounds/' + id); message.success('已删除，配置推送中…'); await load(); pollSyncStatus() }
  catch (e: any) { message.error(e.message) }
}

async function toggleInbound(n: any) {
  try {
    const o = jp(n.options)
    await apiPut('/api/admin/sb/inbounds/' + n.id, { type: n.type, tag: n.tag, listen: n.listen || '::', listen_port: n.listen_port, tls_id: n.tls_id, server_id: n.server_id, enabled: !n.enabled, upstream_inbound_id: n.upstream_inbound_id || 0, options: JSON.stringify(o) })
    n.enabled = !n.enabled
    pollSyncStatus()
  } catch (e: any) { message.error(e.message) }
}

// 端口连通性测试
async function checkPort(n: any) {
  portChecking.value = n.id
  try {
    const r = await apiGet<any>(`/api/admin/sb/port-check?server_id=${n.server_id || 0}&port=${n.listen_port}`)
    portResult.value[n.id] = r
  } catch (e: any) {
    portResult.value[n.id] = { reachable: false, error: e.message || '测试失败' }
  } finally {
    portChecking.value = null
  }
}

// 批量启停
async function batchToggle(enable: boolean) {
  const targets = inbounds.value.filter(n => checkedIds.value.has(n.id))
  let ok = 0
  for (const n of targets) {
    if (n.enabled === enable) { ok++; continue }
    try {
      const o = jp(n.options)
      await apiPut('/api/admin/sb/inbounds/' + n.id, { type: n.type, tag: n.tag, listen: n.listen || '::', listen_port: n.listen_port, tls_id: n.tls_id, server_id: n.server_id, enabled: enable, upstream_inbound_id: n.upstream_inbound_id || 0, options: JSON.stringify(o) })
      n.enabled = enable
      ok++
    } catch {}
  }
  checkedIds.value = new Set()
  message.success(`${ok} 个入站已${enable ? '启用' : '停用'}`)
}

async function batchDelete() {
  const ids = [...checkedIds.value]
  if (!ids.length) return
  // 同一批里被一起删掉的中转不必提示——它们自己也不存在了。
  const orphaned = inbounds.value.filter(i => ids.includes(i.upstream_inbound_id) && !ids.includes(i.id))
  let content = `确定删除选中的 ${ids.length} 个入站？绑定它们的自建节点会一并移除，此操作不可撤销。`
  if (orphaned.length) {
    content += `另外 ${orphaned.map(i => `「${i.tag}」`).join('、')} 以其中的入站为落地，`
      + '删除后将改为从各自所在机器直连出网，出口 IP 会变。'
  }
  dialog.error({
    title: '确认批量删除入站',
    content,
    positiveText: '删除', negativeText: '取消',
    onPositiveClick: async () => {
      let ok = 0
      for (const id of ids) {
        try { await apiDelete('/api/admin/sb/inbounds/' + id); ok++ } catch {}
      }
      checkedIds.value = new Set()
      message.success(`${ok} 个入站已删除`)
      await load()
    },
  })
}

// ========== Preview ==========
const checkLoading = ref(false)
const checkResult = ref<any>(null)

async function loadPreview() {
  previewLoading.value = true
  checkResult.value = null // stale check no longer matches the refreshed config
  try {
    const url = previewSid.value ? '/api/admin/sb/preview?server_id=' + previewSid.value : '/api/admin/sb/preview'
    // 预览接口直接返回原始 sing-box 配置文档（无 {code,data,msg} 信封），
    // 必须用 apiGetRaw 取整个响应体，否则 .data 拆封会得到 null。
    previewJson.value = JSON.stringify(await apiGetRaw(url), null, 2)
  } catch (e: any) {
    // Don't swallow: a blank preview with no reason is what makes "预览是空的"
    // impossible to diagnose. Surface the server's error and clear stale output.
    previewJson.value = ''
    message.error('生成预览失败：' + (e?.message || e))
  } finally { previewLoading.value = false }
}

// 切换机器后清掉上一台机器的校验结果，避免结果与当前预览不一致。
function onPreviewServerChange() {
  previewPicked = true // 用户已显式选机器，别让 tab 重访逻辑覆盖
  checkResult.value = null
  loadPreview()
}

// 正确性检查：让后端用生成配置跑 `sing-box check`（与真正下发前的校验同一道关卡）。
async function runCheck() {
  checkLoading.value = true
  try {
    const url = previewSid.value ? '/api/admin/sb/check?server_id=' + previewSid.value : '/api/admin/sb/check'
    checkResult.value = await apiGet<any>(url)
    if (checkResult.value?.ok) message.success('配置校验通过')
    else message.warning('配置存在问题，详见下方结果')
  } catch (e: any) {
    message.error('校验失败：' + (e?.message || e))
  } finally { checkLoading.value = false }
}

// 一键复制当前预览配置。剪贴板 API 在非 HTTPS 场景可能不可用，退回 execCommand。
async function copyPreview() {
  const text = previewJson.value
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    message.success('配置已复制')
  } catch {
    try {
      const ta = document.createElement('textarea')
      ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'
      document.body.appendChild(ta); ta.select(); document.execCommand('copy'); document.body.removeChild(ta)
      message.success('配置已复制')
    } catch { message.error('复制失败，请手动选择文本复制') }
  }
}

function copy(text: string) { if (text) { navigator.clipboard.writeText(text); message.success('已复制') } }

// ========== Load ==========
async function load() {
  loading.value = true
  try {
    const [t, i, s, e, c] = await Promise.all([apiList('/api/admin/sb/tls'), apiList('/api/admin/sb/inbounds'), apiList('/api/admin/servers'), apiList('/api/admin/sb/egresses'), apiList('/api/admin/certs').catch(() => [])])
    tlsList.value = t; inbounds.value = i; servers.value = s; egresses.value = e; certList.value = c
    // 首次加载后默认展开所有机器；之后保留用户的折叠状态。
    if (!expandedInit) { expandedMachines.value = machines.value.map(m => m.id); expandedInit = true }
  } catch {} finally { loading.value = false }
}
</script>

<style scoped>
.page-head { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.page-head .page-title { margin: 0; }
.page-head .page-sub { margin:4px 0 0; }
.page-head .n-button { margin-left: auto; }
.page-title { font-size: 21px; margin-bottom: 16px; }
.sb-overview { display:grid; grid-template-columns:repeat(5,minmax(0,1fr)); gap:10px; margin-bottom:18px; }
.sb-overview button { display:flex; align-items:center; gap:10px; min-width:0; padding:11px 12px; border:1px solid var(--border); border-radius:12px; background:var(--card); color:inherit; text-align:left; font:inherit; box-shadow:var(--shadow-xs); cursor:pointer; transition:transform .2s cubic-bezier(.2,.8,.2,1), box-shadow .2s ease, border-color .2s ease; }
.sb-overview button:hover { transform:translateY(-2px); border-color:var(--border-strong); box-shadow:var(--shadow-sm); }
.sb-overview button > span:last-child { display:flex; min-width:0; flex-direction:column; }
.sb-overview b { color:var(--text); font-size:18px; line-height:1.15; letter-spacing:-.02em; }
.sb-overview small { overflow:hidden; margin-top:3px; color:var(--text-3); font-size:10.5px; white-space:nowrap; text-overflow:ellipsis; }
.sb-icon { display:grid; place-items:center; flex:none; width:32px; height:32px; border-radius:10px; background:#e8eef3; color:#52606c; font-size:10px; font-weight:700; }
.sb-icon.mint,.sb-icon.ok { background:#e5f3ed; color:#39715a; }
.sb-icon.amber { background:#f8efdc; color:#89651f; }
.sb-icon.violet { background:#eeeaf5; color:#685484; }
.sb-icon.danger { background:#f7e8e6; color:#a24d48; }
:deep(.n-drawer-content-body) { display: flex; flex-direction: column; }

.form-tip { font-size: 12px; color: var(--text-3, #999); margin-top: 4px; line-height: 1.5; }

/* 名称普遍很长（「DMIT洛杉矶-代理出口-300M体验」这种），全站默认的单行省略号
   会把区分度最高的尾巴切掉，这一页改成换行显示，宁可卡片高一点。 */
.list-card .lc-title.wrap { white-space: normal; overflow: visible; text-overflow: clip; word-break: break-all; line-height: 1.35; }
/* 多了一组排序按钮，窄卡片里要能换行，否则最后一个按钮被挤出卡片外。 */
.list-card .lc-foot { flex-wrap: wrap; }
.lc-order { margin-left: auto; display: inline-flex; gap: 2px; }

/* 下发失败原因 */
.sync-err {
  margin: -2px 0 10px;
  padding: 7px 10px;
  border-radius: 6px;
  background: rgba(208, 48, 80, 0.09);
  color: #d03050;
  font-size: 12px;
  line-height: 1.6;
  word-break: break-all;
}
.sync-time { font-size: 11px; color: var(--text-3, #999); }

.egress-test { font-size: 12px; margin: 6px 0 2px; padding: 6px 8px; border-radius: 6px; line-height: 1.5; word-break: break-all; }
.egress-test.ok { background: rgba(24, 160, 88, 0.1); color: #18a058; }
.egress-test.err { background: rgba(208, 48, 80, 0.1); color: #d03050; }
.probe-hint { margin-top: 4px; opacity: 0.85; }
.import-head { font-size: 13px; font-weight: 600; margin: 14px 0 6px; }
.import-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 6px 0; border-top: 1px solid var(--n-border-color, rgba(128,128,128,0.18)); font-size: 12px; }
.import-addr { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; word-break: break-all; }
.import-user { opacity: 0.7; word-break: break-all; }

/* 链路拓扑 */
.topo { display: flex; flex-direction: column; gap: 18px; padding: 4px 2px; }
.topo-legend { display: flex; flex-wrap: wrap; gap: 14px; font-size: 12px; color: var(--text-3, #888); }
.topo-legend span { display: inline-flex; align-items: center; gap: 5px; }
.topo-legend .dot { width: 10px; height: 10px; border-radius: 3px; display: inline-block; }
.dot.client { background: #909399; }
.dot.entry { background: #2080f0; }
.dot.landing { background: #f0a020; }
.dot.egress { background: #7c3aed; }
.dot.inet { background: #18a058; }
.topo-machine { border: 1px solid var(--n-border-color, #e6e6ec); border-radius: 8px; padding: 12px 14px; }
.topo-mhead { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.topo-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 6px 0; }
.topo-row + .topo-row { border-top: 1px dashed var(--n-border-color, #ececf2); }
.topo-row.off { opacity: 0.42; }
.topo-node { display: inline-flex; align-items: baseline; gap: 6px; padding: 4px 10px; border-radius: 7px; font-size: 13px; border: 1px solid transparent; white-space: nowrap; }
.topo-node.client { background: rgba(144, 147, 153, 0.14); color: var(--text-2, #666); }
.topo-node.entry { background: rgba(32, 128, 240, 0.12); border-color: rgba(32, 128, 240, 0.35); }
.topo-node.landing { background: rgba(240, 160, 32, 0.13); border-color: rgba(240, 160, 32, 0.4); }
.topo-node.egress { background: rgba(124, 58, 237, 0.11); border-color: rgba(124, 58, 237, 0.38); }
.topo-node.inet { background: rgba(24, 160, 88, 0.12); color: #18a058; }
.topo-node b { font-weight: 650; }
.topo-proto { font-size: 11px; opacity: 0.75; }
.topo-port, .topo-loc { font-size: 11px; opacity: 0.6; }
.topo-arrow { color: var(--text-3, #aaa); font-size: 13px; user-select: none; }
.topo-arrow.relay { color: #f0a020; font-weight: 600; font-size: 12px; }
.topo-arrow.egress { color: #7c3aed; font-weight: 600; font-size: 12px; }
.topo-arrow.relay.warn { color: #d03050; }
.topo-actions { margin-left: auto; display: inline-flex; gap: 6px; }
@media (max-width: 600px) { .topo-actions { margin-left: 0; } }

.sni-result {
  width: 100%;
  padding: 8px 12px;
  border-radius: 6px;
  font-size: 13px;
  line-height: 1.5;
  border: 1px solid var(--n-border-color, #e0e0e6);
  background: var(--n-color, #f7f7fa);
}
.sni-result.ok {
  border-color: #18a058;
  background: rgba(24, 160, 88, 0.08);
  color: #18a058;
}
.sni-result.partial {
  border-color: #f0a020;
  background: rgba(240, 160, 32, 0.08);
  color: #b88200;
}
.sni-result.unreachable {
  border-color: #d03050;
  background: rgba(208, 48, 80, 0.08);
  color: #d03050;
}
.sni-result b { margin-right: 4px; }

.port-result {
  margin-top: 6px;
  padding: 4px 8px;
  border-radius: 4px;
  font-size: 12px;
  line-height: 1.4;
}
.port-result.ok { background: rgba(24, 160, 88, 0.08); color: #18a058; }
.port-result.err { background: rgba(208, 48, 80, 0.08); color: #d03050; }

/* 配置正确性检查结果 */
.check-result {
  margin: 0 0 12px;
  padding: 10px 14px;
  border-radius: 8px;
  border: 1px solid var(--n-border-color, #e0e0e6);
  font-size: 13px;
}
.check-result.ok { border-color: #18a058; background: rgba(24, 160, 88, 0.07); }
.check-result.err { border-color: #d03050; background: rgba(208, 48, 80, 0.07); }
.check-head { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
.check-result.ok .check-head b { color: #18a058; }
.check-result.err .check-head b { color: #d03050; }
.check-meta { font-size: 12px; color: var(--text-3, #999); }
.check-warn { margin: 8px 0 0; padding-left: 18px; color: #b88200; line-height: 1.6; }
.check-out {
  margin: 8px 0 0;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--n-color, rgba(0, 0, 0, 0.04));
  font-family: var(--font-mono, ui-monospace, Menlo, Consolas, monospace);
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 220px;
  overflow: auto;
}

/* 按机器分组：克制的白卡 + 浅描边，与全站一致 */
.machine-list :deep(.n-collapse-item) {
  border: 1px solid var(--border);
  border-radius: 12px;
  margin-bottom: 12px;
  background: var(--card);
  overflow: hidden;
}
.machine-list :deep(.n-collapse-item:not(:first-child)) { margin-top: 0; }
.machine-list :deep(.n-collapse-item__header) {
  padding: 12px 14px !important;
  border-radius: 12px 12px 0 0;
}
.machine-list :deep(.n-collapse-item--active > .n-collapse-item__header) {
  border-bottom: 1px solid var(--border);
}
.machine-list :deep(.n-collapse-item__content-inner) {
  padding: 14px !important;
  background: var(--bg-soft);
}
/* 折叠的 ACME 区块：轻量收纳 */
.acme-collapse { border: 1px solid var(--border); border-radius: 8px; background: var(--bg-soft); padding: 2px 12px; }
.acme-collapse :deep(.n-collapse-item) { margin-top: 0; border-top: none; }
.acme-collapse :deep(.n-collapse-item__header) { padding: 10px 0 !important; }

.machine-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
.machine-name { font-weight: 650; font-size: 15px; color: var(--text); }
.machine-host { font-size: 12px; color: var(--text-3); }
.machine-extra { display: flex; align-items: center; gap: 6px; }
@media (max-width: 640px) {
  .sb-overview { grid-template-columns:repeat(2,minmax(0,1fr)); }
  .sb-overview button:last-child { grid-column:1 / -1; }
  .machine-host { display: none; }
  .machine-extra { gap: 4px; }
}
</style>
