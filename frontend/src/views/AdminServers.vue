<template>
  <div>
    <div class="page-head">
      <div><h2 class="page-title">服务器管理</h2><p class="page-sub">远程机器、SSH 接管、探针与 sing-box 运行版本</p></div>
      <div class="page-actions"><AdminComplianceCheck /><n-button type="primary" @click="openForm()">添加服务器</n-button></div>
    </div>
    <div class="resource-overview">
      <div class="resource-metric"><b>{{ servers.length }}</b><span>全部远程服务器</span></div>
      <div class="resource-metric success"><b>{{ servers.filter(s => s.enabled).length }}</b><span>已启用</span></div>
      <div class="resource-metric"><b>{{ servers.filter(s => s.probe_enabled).length }}</b><span>探针已开启</span></div>
      <div class="resource-metric" :class="{ danger: versions.filter(v => v.too_old || (v.version && !v.has_v2ray_api)).length }"><b>{{ versions.filter(v => v.too_old || (v.version && !v.has_v2ray_api)).length }}</b><span>版本或统计异常</span></div>
    </div>
    <AdminRelayMetering />
    <!-- 各节点实际在跑的 sing-box。数据来自面板本来就在做的能力探测 -->
    <n-card size="small" class="ver-panel" style="margin-bottom:16px;">
      <template #header>
        <div class="vp-head-left">
          <span>节点 sing-box 内核版本</span>
          <span class="vp-req">要求 ≥ {{ minSupported }}</span>
          <span v-if="currentReleaseVersion" class="vp-rel">面板构建 <code>{{ shortVer(currentReleaseVersion) }}</code></span>
        </div>
      </template>
      <template #header-extra>
        <n-space size="small" align="center">
          <n-button size="tiny" type="primary" secondary :loading="verLoading" @click="refreshVersions">重新检测</n-button>
        </n-space>
      </template>

      <details class="nv-help-details">
        <summary class="nv-help-summary">
          <span>内核探测与补丁核验说明（点击展开详情）</span>
          <span class="nv-help-tags">
            <span v-if="visionFixedVersion">Vision <code>{{ visionFixedVersion }}</code></span>
            <span v-if="transportFixedVersion">WS/HTTPUpgrade <code>{{ transportFixedVersion }}</code></span>
            <span v-if="currentReleaseVersion">当前发布 <code>{{ currentReleaseVersion }}</code></span>
          </span>
        </summary>
        <div class="nv-help-box">
          这里显示每台机器最近检测到的内核版本；普通重新检测读取已安装文件，更换磁盘文件不代表运行中的进程已更新。
          逐用户中转的 Vision、WebSocket/HTTPUpgrade 路径还会在下发前后核验实际运行的内核；预检不通过时不切换该路径，运行能力复核通过后才确认下发。
          面板生成的配置要求 <b>sing-box ≥ {{ minSupported }}</b>；低于这个版本，节点的
          <code>sing-box check</code> 会失败，面板会<b>停止向它下发任何配置</b>（旧配置继续跑，所以表面看不出来）。
          「重装」装的是<b>面板自己发布的构建</b>（随面板版本走，当前标识<span v-if="currentReleaseVersion"> <code>{{ currentReleaseVersion }}</code></span>，含流量统计所需的 <code>v2ray_api</code> 与 Trojan 分段握手维护补丁）。
          官方版不带这个插件，无法提供所需流量统计，也不含 Trojan 维护补丁。
          已安装的历史审定标记（Vision <code v-if="visionFixedVersion">{{ visionFixedVersion }}</code>、WS/HTTPUpgrade <code v-if="transportFixedVersion">{{ transportFixedVersion }}</code>，以及旧 1.14.2 Trojan 标记）仍然算具备相应能力；不能仅凭官方版本号较新判断支持。
        </div>
      </details>

      <div v-if="versions.length" class="nv-table">
        <div v-for="n in versions" :key="n.server_id" class="nv-row">
          <div class="nv-col-node">
            <span class="nv-dot" :class="n.too_old || (n.version && !n.has_v2ray_api) || n.error ? 'err' : (n.version ? 'ok' : 'idle')" />
            <span class="nv-name">{{ n.name }}</span>
            <code v-if="n.host" class="nv-host">{{ n.host }}</code>
            <n-tag v-if="n.server_id === 0" size="tiny" :bordered="false">本机</n-tag>
          </div>
          <div class="nv-col-ver">
            <span class="nv-ver-badge" :title="n.version || '未知'">{{ shortVer(n.version) }}</span>
            <span v-if="verBuildMeta(n.version)" class="nv-ver-sub" :title="n.version">{{ verBuildMeta(n.version) }}</span>
          </div>
          <div class="nv-col-caps">
            <n-tag v-if="n.too_old" type="error" size="tiny" :bordered="false">版本过低</n-tag>
            <n-tag v-else-if="!n.version" type="default" size="tiny" :bordered="false">未安装 / 未知</n-tag>
            <n-tag v-if="n.version && !n.has_v2ray_api" type="error" size="tiny" :bordered="false">缺 v2ray_api</n-tag>
            <template v-if="n.version">
              <span class="cap-pill" :class="{ on: n.has_vision_framing_fix }">
                {{ n.has_vision_framing_fix ? '版本含 Vision 修复标记' : '未确认 Vision 修复' }}
              </span>
              <span class="cap-pill" :class="{ on: n.has_transport_read_buffer_fix }">
                {{ n.has_transport_read_buffer_fix ? '版本含 WS/HTTPUpgrade 修复标记' : '未确认 WS/HTTPUpgrade 修复' }}
              </span>
              <span class="cap-pill" :class="{ on: n.has_trojan_handshake_fix }">
                {{ n.has_trojan_handshake_fix ? '版本含 Trojan 分段握手修复标记' : '未确认 Trojan 分段握手修复' }}
              </span>
            </template>
          </div>
          <div class="nv-col-side">
            <span v-if="n.checked_at" class="nv-time">{{ fmtDateTime(n.checked_at) }}</span>
            <n-button size="tiny" secondary :disabled="!n.upgradable || n.upgrading" :loading="n.upgrading"
                      @click="confirmUpgrade(n)">
              {{ n.upgrading ? '安装中' : (n.version ? '重装内核' : '安装内核') }}
            </n-button>
          </div>
          <!-- 安装是后台跑的（要往节点上拉 ~60MB 的二进制），这里显示脚本的输出。 -->
          <div v-if="n.upgrading" class="nv-err" style="color:var(--text-3);">
            正在安装…（往节点下载 sing-box 约 60MB，通常 1–3 分钟；离开本页不影响安装）
          </div>
          <div v-else-if="n.upgrade_error" class="nv-err">
            安装失败：<pre class="nv-out">{{ n.upgrade_error }}</pre>
          </div>
          <div v-if="n.error" class="nv-err">探测失败：{{ n.error }}</div>
          <div v-else-if="n.version && !n.has_v2ray_api" class="nv-err">
            该版本不含 <code>with_v2ray_api</code> 插件 —— 这台机器的流量<b>统计不到</b>，配额也不会生效（界面上一直显示 0）。
            点「重装内核」换成面板发布的版本即可；官方发布版不带这个插件。
          </div>
        </div>
      </div>
      <n-empty v-else-if="!verLoading" description="暂无数据，点「重新检测」" style="padding:20px 0;" />
    </n-card>

    <n-spin :show="loading">
      <div v-if="servers.length" class="srv-grid">
        <div v-for="(s, idx) in servers" :key="s.id" class="srv-item-card" :class="{ 'is-disabled': !s.enabled }">
          <div class="sic-head">
            <div class="sic-title-row">
              <i class="order-no">{{ idx + 1 }}</i>
              <span class="sic-name" :title="s.name">{{ s.name || '—' }}</span>
              <n-tag v-if="!s.enabled" type="warning" size="tiny" :bordered="false">已禁用</n-tag>
            </div>
            <div class="sic-order">
              <n-button size="tiny" quaternary circle :disabled="idx === 0 || reordering" title="前移" @click="moveServer(idx, -1)">←</n-button>
              <n-button size="tiny" quaternary circle :disabled="idx === servers.length - 1 || reordering" title="后移" @click="moveServer(idx, 1)">→</n-button>
              <n-switch size="small" :value="!!s.enabled" :loading="togglingServer === s.id" @update:value="v => toggleServerEnabled(s, v)" />
            </div>
          </div>

          <div class="sic-info-grid">
            <div class="sic-info-cell">
              <span class="k">SSH 主机</span>
              <code class="v">{{ s.host }}:{{ s.port }}</code>
            </div>
            <div class="sic-info-cell">
              <span class="k">对外域名</span>
              <span v-if="s.domain" class="v domain-chip">{{ s.domain }}</span>
              <span v-else class="v muted">未绑定（使用 IP）</span>
            </div>
            <div v-if="s.domain_v6 || s.host_v6" class="sic-info-cell">
              <span class="k">IPv6 接入</span>
              <span class="v domain-chip">{{ s.domain_v6 || s.host_v6 }}</span>
            </div>
            <div class="sic-info-cell">
              <span class="k">监控与内核</span>
              <div class="v tags-inline">
                <n-tag :type="s.probe_enabled ? 'success' : 'default'" size="tiny" :bordered="false">
                  探针 {{ s.probe_enabled ? '已开启' : '未开启' }}
                </n-tag>
                <span v-if="serverVerOf(s.id)" class="ver-mini-tag" :title="serverVerOf(s.id)">
                  {{ shortVer(serverVerOf(s.id)) }}
                </span>
              </div>
            </div>
          </div>

          <div class="sic-foot">
            <n-button size="tiny" type="primary" secondary @click="openTrafficAnalysis(s)">流量分析</n-button>
            <n-button size="tiny" type="primary" secondary @click="router.push({ path: '/admin/setup', query: { server: String(s.id) } })">向导配置</n-button>
            <n-button size="tiny" secondary @click="openDnsModal(s)">子域名</n-button>
            <n-button size="tiny" @click="openForm(s)">编辑</n-button>
            <span style="flex:1;" />
            <n-dropdown trigger="click" :options="serverMoreOpts(s)" @select="(k: string) => onServerMore(k, s)">
              <n-button size="tiny" quaternary :loading="clearing === s.id">更多 ▾</n-button>
            </n-dropdown>
          </div>
        </div>
      </div>
      <n-empty v-else-if="!loading" description="下一步：添加服务器，或在面板本机安装 sing-box 后重新检测" style="padding:40px 0;"><template #extra><n-button @click="openForm()">添加服务器</n-button></template></n-empty>
    </n-spin>

    <n-modal v-model:show="showForm" preset="card" :title="editing?'编辑服务器':'添加服务器'" style="max-width:560px;">
      <n-form label-placement="left" label-width="100">
        <n-form-item label="名称"><n-input v-model:value="form.name" /></n-form-item>
        <n-form-item label="主机"><n-input v-model:value="form.host" placeholder="IP 或域名（SSH 连接始终走此地址）" /></n-form-item>
        <n-form-item label="对外域名（可选）"><n-input v-model:value="form.domain" placeholder="如 taxi.example.com（订阅和 HTTPS 代理优先使用此域名）" /></n-form-item>
        <n-form-item label="IPv6 订阅地址（可选）"><n-input v-model:value="form.host_v6" placeholder="IPv6 地址或 AAAA 域名，留空则不生成" /></n-form-item>
        <n-form-item label="IPv6 对外域名（可选）"><n-input v-model:value="form.domain_v6" placeholder="如 taxi-v6.example.com（覆盖上面 IPv6 订阅地址）" /></n-form-item>
        <div style="margin:-12px 0 12px;font-size:12px;color:var(--text-3)">填写后订阅会为本机每个节点额外生成一条「原名-v6」节点（建议填字面量 IPv6 地址，或使用下方 IPv6 对外域名），原节点不变；SSH 仍使用上面的主机。本机直出流量会按客户端连入的地址族自动优先走 IPv4 或 IPv6（中转落地不受影响）。设置「对外域名」后，订阅链接与 HTTPS 代理将自动优先使用域名（配合泛域名证书即可让 1Panel / Docker / curl 直接信任）；也可以在服务器卡片点「子域名」通过 Cloudflare Token 一键创建解析。</div>
        <n-form-item label="SSH 端口"><n-input-number v-model:value="form.port" :min="1" :max="65535" style="width:100%;" /></n-form-item>
        <n-form-item label="SSH 用户"><n-input v-model:value="form.ssh_user" placeholder="root" @update:value="onSshUserInput" /></n-form-item>
        <n-form-item label="sudo 提权">
          <div style="width:100%;">
            <n-switch v-model:value="form.use_sudo" />
            <div class="hint">
              非 root 账号必须打开：写配置、装配置、重启服务都要 root。
              <b>需要免密 sudo（NOPASSWD），等同于把 root 交给面板</b>；填了密码则用密码提权。
            </div>
          </div>
        </n-form-item>
        <n-form-item v-if="form.use_sudo" label="sudo 密码"><n-input v-model:value="form.sudo_password" type="password" show-password-on="click" placeholder="留空并保存 = 改用免密 sudo" /></n-form-item>
        <n-form-item label="SSH 密码"><n-input v-model:value="form.ssh_password" type="password" show-password-on="click" placeholder="与密钥二选一" /></n-form-item>
        <n-form-item label="SSH 私钥">
          <div style="width:100%;">
            <n-radio-group v-model:value="keyMode" size="small">
              <n-radio-button value="paste">粘贴</n-radio-button>
              <n-radio-button value="file">从文件选择</n-radio-button>
            </n-radio-group>
            <n-input v-if="keyMode==='paste'" v-model:value="form.ssh_key" type="textarea" :rows="3"
                     placeholder="-----BEGIN ... PRIVATE KEY-----" style="margin-top:8px;" />
            <template v-else>
              <n-select v-model:value="form.ssh_key_path" :options="keyOptions" clearable
                        :loading="keysLoading" placeholder="选择一个私钥文件" style="margin-top:8px;" />
              <div class="hint">
                目录：<code>{{ keyDir || '未配置' }}</code>
                <template v-if="!keysConfigured"> —— 未配置，设置 <code>QZ_SSH_KEY_DIR</code> 后可用。</template>
                <template v-else-if="!keyFiles.length"> —— 目录不存在或还没有私钥文件；把私钥放进去并 <code>chmod 600</code>。</template>
                <br>私钥不经过浏览器、也不入库。容器内面板以 uid 10001 运行，挂进去的密钥要它能读（<code>chown 10001</code>）。
                <template v-if="form.ssh_key"><br><b>已选文件时，上面粘贴过的私钥不再使用。</b></template>
              </div>
            </template>
          </div>
        </n-form-item>
        <n-form-item label="密钥密码"><n-input v-model:value="form.ssh_key_pass" type="password" show-password-on="click" placeholder="已保存时显示 ***；清空并保存可删除" /></n-form-item>
        <n-form-item label="配置路径"><n-input v-model:value="form.config_path" placeholder="/etc/sing-box/config.json" /></n-form-item>
        <n-form-item label="systemd 单元"><n-input v-model:value="form.systemd_unit" placeholder="sing-box" /></n-form-item>
        <n-form-item label="sing-box 路径"><n-input v-model:value="form.sing_box_bin" placeholder="/usr/local/bin/sing-box" /></n-form-item>
        <n-form-item label="v2ray 监听"><n-input v-model:value="form.v2ray_listen" placeholder="127.0.0.1:18080" /></n-form-item>
        <n-form-item label="启用">
          <div>
            <n-switch v-model:value="form.enabled" />
            <div class="form-tip">关闭后这台机器上的节点会从订阅中隐藏，机器上的 sing-box 不会被停止；重新打开即恢复。</div>
          </div>
        </n-form-item>
      </n-form>
      <n-space>
        <n-button type="primary" :loading="saving" @click="handleSave">保存</n-button>
        <n-button @click="handleTest" :loading="testing">测试连接</n-button>
      </n-space>
    </n-modal>

    <n-modal v-model:show="showDns" preset="card" :title="`配置子域名 · ${dnsServer?.name || ''}`" style="max-width:560px;">
      <n-spin :show="cfZonesLoading">
        <n-alert v-if="cfZonesLoaded && !cfConfigured" type="warning" :bordered="false" style="margin-bottom:12px;">
          尚未配置 Cloudflare API Token。请先到「系统设置 → 证书 / ACME」填写具备 <b>Zone · DNS · Edit</b> 与 <b>Zone · Read</b> 权限的 Token，即可在这里一键写灰云解析；也可以取消下方同步开关仅保存手动解析好的域名。
        </n-alert>
        <n-alert v-else-if="cfZonesError" type="warning" :bordered="false" style="margin-bottom:12px;">
          拉取 Cloudflare 根域名列表失败：{{ cfZonesError }}（仍可手动输入完整子域名）
        </n-alert>
        <n-form label-placement="left" label-width="110">
          <n-form-item label="服务器 IP">
            <div style="font-size:12px;color:var(--text-2);line-height:1.6;">
              IPv4：<code>{{ dnsServer?.host || '—' }}</code>
              <template v-if="dnsServer?.host_v6"><br>IPv6：<code>{{ dnsServer.host_v6 }}</code></template>
            </div>
          </n-form-item>
          <n-form-item v-if="cfZones.length" label="快捷拼接">
            <div style="display:flex;gap:8px;width:100%;">
              <n-input v-model:value="dnsForm.prefix" placeholder="前缀，如 taxi / dmit" style="flex:1;" @update:value="syncPrefixToDomain" />
              <n-select v-model:value="dnsForm.zone" :options="cfZoneOptions" placeholder="选择根域名" style="width:190px;" @update:value="syncPrefixToDomain" />
            </div>
          </n-form-item>
          <n-form-item label="主子域名">
            <n-input v-model:value="dnsForm.domain" placeholder="如 taxi.example.com" />
          </n-form-item>
          <n-form-item label="写入 IPv4 (A)">
            <div style="width:100%;">
              <n-switch v-model:value="dnsForm.sync_v4" :disabled="!dnsHasIPv4" />
              <span style="margin-left:8px;font-size:12px;color:var(--text-3);">
                {{ dnsHasIPv4 ? `自动在 Cloudflare 写入灰云 A 记录 → ${dnsServer?.host}` : '当前主机不是 IPv4 地址，无法自动写入 A 记录' }}
              </span>
            </div>
          </n-form-item>
          <template v-if="dnsHasIPv6">
            <n-form-item label="写入 IPv6 (AAAA)">
              <div style="width:100%;">
                <n-switch v-model:value="dnsForm.sync_v6" />
                <span style="margin-left:8px;font-size:12px;color:var(--text-3);">
                  自动写入灰云 AAAA 记录 → {{ dnsServer?.host_v6 }}
                </span>
              </div>
            </n-form-item>
            <n-form-item label="IPv6 独立子域名">
              <div style="width:100%;">
                <n-input v-model:value="dnsForm.domain_v6" placeholder="可选，如 taxi-v6.example.com（留空则把 AAAA 写到主子域名）" />
                <div class="form-tip">填写独立 IPv6 子域名可配合轻舟的双栈分流与「-v6」节点；留空且开启写入 AAAA 时，主子域名会同时带有 A 与 AAAA 记录。</div>
              </div>
            </n-form-item>
          </template>
          <n-form-item v-if="dnsForm.sync_v4 || dnsForm.sync_v6" label="清理旧记录">
            <div style="width:100%;">
              <n-switch v-model:value="dnsForm.cleanup_old" />
              <span style="margin-left:8px;font-size:12px;color:var(--text-3);">改名时自动清理 Cloudflare 上旧的轻舟托管记录（不会误删手动记录）</span>
            </div>
          </n-form-item>
        </n-form>

        <n-alert v-if="dnsResult" type="success" :bordered="false" style="margin-bottom:12px;">
          <div><b>已保存并同步完成</b></div>
          <div v-for="(r, idx) in dnsResult.results || []" :key="idx" style="font-size:12px;margin-top:4px;">
            {{ r.type }} <code>{{ r.name }}</code> → <code>{{ r.content }}</code>（{{ r.action === 'created' ? '已新建' : r.action === 'updated' ? '已更新' : '无需变更' }}，灰云仅 DNS）
          </div>
          <div v-if="dnsResult.matching_certs?.length" style="font-size:12px;margin-top:6px;">
            ✅ 证书中心已有可覆盖该域名的真实证书：<b>{{ dnsResult.matching_certs.map((c:any) => `${c.name} (${c.domain})`).join('、') }}</b>，可直接去「sing-box → TLS 配置」引用并绑定到 HTTPS 代理或 Hy2/Trojan 入站。
          </div>
          <div v-else-if="dnsResult.suggested_wildcard" style="font-size:12px;margin-top:6px;">
            💡 证书中心暂无覆盖该域名的 CA 证书：建议去「证书管理」用 Cloudflare DNS 申请一张泛域名证书 <code>{{ dnsResult.suggested_wildcard }}</code>（全机房所有子域名通用、自动续期）。
          </div>
        </n-alert>

        <n-space justify="space-between" style="width:100%;">
          <n-space>
            <n-button type="primary" :loading="dnsSaving" @click="submitDns(false)">保存并同步 DNS</n-button>
            <n-button @click="showDns = false">关闭</n-button>
          </n-space>
          <n-button v-if="dnsServer?.domain || dnsServer?.domain_v6" type="error" secondary :loading="dnsDeleting" @click="confirmClearDns">清除子域名</n-button>
        </n-space>
      </n-spin>
    </n-modal>

    <n-drawer v-model:show="showTraffic" :width="trafficDrawerWidth" placement="right" @after-leave="disposeTrafficChart">
      <n-drawer-content :title="`周期流量分析${trafficAnalysis?.server_name ? ' · ' + trafficAnalysis.server_name : ''}`" closable>
        <n-spin :show="trafficLoading">
          <template v-if="trafficAnalysis">
            <n-alert v-if="trafficIncomplete" type="warning" :bordered="false" style="margin-bottom:14px;">
              当前周期的设备流量仅从 {{ fmtDateTime(trafficAnalysis.usage.coverage_start) }} 开始采集；未校准前，周期总量和容量预估可能偏低。
            </n-alert>
            <div class="traffic-summary-grid">
              <div class="traffic-summary-card"><span>本周期已用</span><b>{{ fmtBytes(trafficAnalysis.usage.total) }}</b><small>{{ trafficModeLabel(trafficAnalysis.accounting_mode) }}</small></div>
              <div class="traffic-summary-card"><span>剩余流量</span><b>{{ trafficAnalysis.limit_bytes > 0 ? fmtBytes(trafficRemaining) : '未设上限' }}</b><small>重置 {{ fmtDateTime(trafficAnalysis.next_reset) }}</small></div>
              <div class="traffic-summary-card"><span>近期日均</span><b>{{ trafficAnalysis.projection.available ? fmtBytes(trafficAnalysis.projection.daily_rate_bytes) : '—' }}</b><small>{{ trafficAnalysis.projection.available ? `基于 ${trafficAnalysis.projection.sample_days.toFixed(1)} 天` : trafficAnalysis.projection.reason }}</small></div>
              <div class="traffic-summary-card capacity"><span>预计还能新增</span><b>{{ trafficAnalysis.projection.available ? trafficAnalysis.projection.estimated_additional_users + ' 人' : '—' }}</b><small>保证现有活跃用户用到本周期结束</small></div>
            </div>

            <div v-if="trafficAnalysis.limit_bytes > 0" class="quota-overview">
              <div><span>周期额度消耗</span><b>{{ trafficPercent.toFixed(1) }}%</b></div>
              <n-progress type="line" :percentage="Math.min(trafficPercent, 100)" :show-indicator="false" :height="8" :color="trafficPercent >= 90 ? '#c2685c' : trafficPercent >= 70 ? '#bf9540' : '#5c8b70'" />
              <p v-if="trafficAnalysis.projection.available">
                按近期速度，周期结束预计使用 {{ fmtBytes(trafficAnalysis.projection.projected_cycle_total_bytes) }}。
                <template v-if="trafficAnalysis.projection.estimated_exhaustion_at && trafficAnalysis.projection.estimated_exhaustion_at < trafficAnalysis.next_reset">预计 {{ fmtDateTime(trafficAnalysis.projection.estimated_exhaustion_at) }} 用尽额度。</template>
              </p>
            </div>

            <section class="traffic-section">
              <div class="traffic-section-head"><div><h3>周期流量趋势</h3><p>物理网卡 IN / OUT 按日统计；折线是当前服务商计费口径下的日用量。</p></div></div>
              <div ref="trafficChartEl" class="traffic-chart" />
              <n-empty v-if="!trafficAnalysis.daily?.length" description="本周期暂无可绘制的探针数据" style="padding:24px 0;" />
            </section>

            <AdminServiceTraffic v-if="trafficAnalysis.service" :service="trafficAnalysis.service" />
            <section v-else class="traffic-section">
              <div class="traffic-section-head">
                <div><h3>流量消耗来源</h3><p>来自本机 sing-box 的用户级统计，不包含系统更新、SSH 等非代理流量。</p></div>
                <span v-if="trafficAnalysis.attribution.coverage_start" class="coverage-chip">自 {{ fmtDateTime(trafficAnalysis.attribution.coverage_start) }} 归因</span>
              </div>
              <div class="source-overview">
                <span><b>{{ trafficAnalysis.attribution.active_users }}</b> 位活跃用户</span>
                <span>已归因 <b>{{ fmtBytes(trafficAnalysis.attribution.total) }}</b></span>
                <span v-if="trafficAnalysis.projection.available">人均日消耗 <b>{{ fmtBytes(trafficAnalysis.projection.per_user_daily_bytes) }}</b></span>
              </div>
              <div v-if="trafficAnalysis.attribution.sources?.length" class="traffic-sources">
                <div v-for="(source, idx) in trafficAnalysis.attribution.sources" :key="source.user_id" class="traffic-source-row">
                  <span class="source-rank">{{ idx + 1 }}</span>
                  <div class="source-main">
                    <div><b>{{ source.username }}</b><span>{{ fmtBytes(source.total) }}</span></div>
                    <div class="source-track"><i :style="{ width: sourcePercent(source.total) + '%' }" /></div>
                    <small>上行 {{ fmtBytes(source.up) }} · 下行 {{ fmtBytes(source.down) }}</small>
                  </div>
                </div>
              </div>
              <n-empty v-else description="尚无按服务器归因的用户流量；升级后会从下一次统计轮询开始积累" style="padding:28px 0;" />
            </section>

            <n-alert type="info" :bordered="false" style="margin-top:14px;">
              “预计还能新增”不是人数硬上限：它用归因窗口内的物理流量 ÷ 活跃用户得到人均日消耗，先预留现有用户到重置日的预计用量，再计算剩余额度可承载的同等用户数。归因不足 6 小时时不出数。
            </n-alert>
          </template>
        </n-spin>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>
<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, nextTick, shallowRef, h } from 'vue'
import { useRouter } from 'vue-router'
import { NSpin, NButton, NModal, NForm, NFormItem, NInput, NInputNumber, NSwitch, NSpace, NTag, NEmpty, NCard, NSelect, NRadioGroup, NRadioButton, NDrawer, NDrawerContent, NAlert, NProgress, NDropdown, useMessage, useDialog } from 'naive-ui'
import { apiList, apiPost, apiPut, apiDelete, apiGet } from '@/api'
import { fmtBytes, fmtDateTime, pct } from '@/utils/format'
import AdminServiceTraffic from '@/components/AdminServiceTraffic.vue'
import AdminRelayMetering from '@/components/AdminRelayMetering.vue'
import AdminComplianceCheck from '@/components/AdminComplianceCheck.vue'
import * as echarts from 'echarts'
const router = useRouter()
const message = useMessage()
const dialog = useDialog()
const servers = ref<any[]>([])
const loading = ref(false); const saving = ref(false); const testing = ref(false)
const reordering = ref(false)
const showForm = ref(false); const editing = ref<any>(null)
const mk = () => ({ name:'', host:'', domain:'', host_v6:'', domain_v6:'', port:22, ssh_user:'root', ssh_password:'', ssh_key:'', ssh_key_pass:'', use_sudo:false, sudo_password:'', ssh_key_path:'', config_path:'/etc/sing-box/config.json', systemd_unit:'sing-box', sing_box_bin:'/usr/local/bin/sing-box', v2ray_listen:'127.0.0.1:18080', enabled:true })
const form = reactive(mk())

// 私钥来源二选一。粘贴走 ssh_key（加密入库），选文件走 ssh_key_path（只存文件名，
// 内容始终留在面板机器上）。后端以文件优先。
const keyMode = ref<'paste'|'file'>('paste')
const keyFiles = ref<any[]>([])
const keyDir = ref(''); const keysConfigured = ref(false); const keysLoading = ref(false)
const keyOptions = computed(() => keyFiles.value.map((f:any) => ({
  // 可读性直接标在选项上：容器内以 uid 10001 跑，宿主机 chmod 600 的 root 私钥
  // 挂进去读不了，是这个功能最常见的失败方式。让人一眼看见，别保存完去猜。
  label: f.name + (!f.readable ? '（面板读不了）' : (!f.mode_ok ? '（权限过松）' : '')),
  value: f.name,
  disabled: !f.readable,
})))
async function loadKeys(){
  keysLoading.value = true
  try{
    const r:any = await apiGet('/api/admin/ssh-keys')
    keyFiles.value = r?.files || []; keyDir.value = r?.dir || ''; keysConfigured.value = !!r?.configured
  }catch{ keyFiles.value = []; keysConfigured.value = false }
  finally{ keysLoading.value = false }
}
function openForm(s?:any){
  editing.value = s||null
  if(s){ Object.assign(form,{ name:s.name, host:s.host, domain:s.domain||'', host_v6:s.host_v6||'', domain_v6:s.domain_v6||'', port:s.port||22, ssh_user:s.ssh_user||'root', ssh_password:'', ssh_key:'', ssh_key_pass:s.ssh_key_pass||'', use_sudo:s.use_sudo??false, sudo_password:s.sudo_password||'', ssh_key_path:s.ssh_key_path||'', config_path:s.config_path||'/etc/sing-box/config.json', systemd_unit:s.systemd_unit||'sing-box', sing_box_bin:s.sing_box_bin||'/usr/local/bin/sing-box', v2ray_listen:s.v2ray_listen||'127.0.0.1:18080', enabled:s.enabled??true }) }
  else { Object.assign(form, mk()) }
  keyMode.value = form.ssh_key_path ? 'file' : 'paste'
  showForm.value = true
  loadKeys()
}
// 改 SSH 用户时替管理员把 sudo 开关拨到通常正确的位置：非 root 必然要提权，
// root 必然不要。只在「人手动改这个输入框」时触发 —— 打开已有服务器走的是
// openForm，不经过这里，所以不会覆盖管理员自己拨过的开关。
function onSshUserInput(v:string){
  form.use_sudo = (v||'').trim() !== 'root' && (v||'').trim() !== ''
}

const togglingServer = ref<number | null>(null)
async function toggleServerEnabled(s:any, enabled:boolean){
  if(togglingServer.value) return
  const prev = s.enabled
  s.enabled = enabled
  togglingServer.value = s.id
  try{
    await apiPut(`/api/admin/servers/${s.id}`, { enabled })
    message.success(enabled ? '已启用' : '已禁用，该机器节点不再出现在订阅中（sing-box 保持运行）')
  }catch(e:any){
    s.enabled = prev
    message.error(e.message || '更新失败')
  }finally{ togglingServer.value = null }
}
async function handleSave(){
  saving.value = true
  try{
    const b:any = {...form}
    if(!b.ssh_password) delete b.ssh_password
    if(!b.ssh_key) delete b.ssh_key
    // 这两个字段必须把空字符串送到后端：更新接口以「字段缺失」表示保留旧值，
    // 以空字符串表示真的清除。编辑时保留的 *** 会由后端换回原密文。
    // sudo 已关闭时密码不再有用途，保存时顺手从库里清掉。
    if(!b.use_sudo) b.sudo_password = ''
    // 切回粘贴就是明确不要文件了，所以显式送空把它清掉（更新接口是「没送的字段
    // 保留原值」，delete 会让旧的文件名留在库里继续生效）。反过来选了文件时不动
    // ssh_key：后端本来就文件优先，而空的 ssh_key 会被下面删掉以免误清密钥。
    if(keyMode.value === 'paste'){ b.ssh_key_path = '' }
    else { delete b.ssh_key }
    if(editing.value) await apiPut(`/api/admin/servers/${editing.value.id}`,b)
    else await apiPost('/api/admin/servers',b)
    message.success('保存成功'); showForm.value=false; editing.value=null; await load()
  }catch(e:any){message.error(e.message)}finally{saving.value=false}
}
async function handleTest(){ testing.value=true; try{ const id=editing.value?.id; if(!id){message.warning('请先保存');return} await apiPost(`/api/admin/servers/${id}/test`); message.success('连接成功') }catch(e:any){message.error(e.message)}finally{testing.value=false} }
async function handleRebuild(id:number){ try{await apiPost(`/api/admin/servers/${id}/rebuild`);message.success('重建成功')}catch(e:any){message.error(e.message)} }

// 清除固定的 SSH 主机密钥并重新信任。
// 面板首次连上一台机器时会记住它的主机密钥，此后每次连接都要求对得上——这是拦住
// 「有人冒充这个 IP 骗走 root 凭据」的那道门。但机器重装系统会重新生成主机密钥，
// 换机复用同一条记录也一样，那时面板就会一直拒连（表现为配置下发失败），而这道门
// 只能从这里打开：host_key 不下发给前端，编辑服务器也不会动它。
// 所以按钮必须带确认：清除等于放弃对这台机器身份的既有判断。
const clearing = ref<number|null>(null)
// 弹窗正文用 pre-line 渲染：naive 的 dialog 把 content 当普通文本塞进一个 div，
// 字符串里的换行会被折掉——这段正好是要分行看的安全提示，挤成一坨就没人读了。
const preLine = (text:string) => () => h('div', { style: 'white-space:pre-line;line-height:1.75;' }, text)
function handleClearHostKey(s:any){
  dialog.warning({
    title: '清除已固定的主机密钥？',
    content: preLine(`面板将忘记「${s.name || s.host}」当前记住的 SSH 主机密钥，并按下一次连接看到的密钥重新记住。\n\n`
      + '✅ 机器重装过系统 / 换了机器 / 供应商做了迁移 → 这是正常操作。\n\n'
      + '⛔ 你想不出它为什么会变 → 先别清。那可能是中间人在冒充这台机器，清掉就等于把 root 凭据交给它。'),
    positiveText: '确认清除并重新连接',
    negativeText: '取消',
    onPositiveClick: async () => {
      clearing.value = s.id
      try{
        const r:any = await apiPost(`/api/admin/servers/${s.id}/clear-host-key`)
        message.success(r?.message || '已重新信任')
        if(r?.fingerprint){
          dialog.success({
            title: '已重新记住这台机器',
            content: preLine(`新指纹：${r.fingerprint}\n\n`
              + '请在这台机器上执行 ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub 核对，对得上说明连的确实是你的机器。\n\n'
              + '接着到「sing-box → 链路拓扑」点「重新下发」，把这段时间没同步上的配置和用户变更推过去。'),
            positiveText: '知道了',
          })
        }
        await load()
      }catch(e:any){ message.error(e.message) }finally{ clearing.value = null }
    },
  })
}
async function handleDelete(id:number){ try{await apiDelete(`/api/admin/servers/${id}`);message.success('已删除');await load()}catch(e:any){message.error(e.message)} }
async function moveServer(idx:number, dir:number){
  const target = idx + dir
  if(target < 0 || target >= servers.value.length || reordering.value) return
  const before = [...servers.value]
  const next = [...servers.value]
  ;[next[idx], next[target]] = [next[target], next[idx]]
  servers.value = next
  reordering.value = true
  try{
    await apiPost('/api/admin/servers/reorder', { ids: next.map(s => s.id) })
  }catch(e:any){ servers.value = before; message.error(e.message) }
  finally{ reordering.value = false }
}
async function load(){loading.value=true;try{servers.value=await apiList('/api/admin/servers')}catch{}finally{loading.value=false}}

// ---- Provider-cycle traffic analysis ----
const showTraffic = ref(false)
const trafficLoading = ref(false)
const trafficAnalysis = ref<any>(null)
const trafficChartEl = ref<HTMLElement|null>(null)
const trafficChart = shallowRef<echarts.ECharts|null>(null)
const trafficDrawerWidth = computed(() => Math.min(typeof window === 'undefined' ? 820 : window.innerWidth * .94, 820))
const trafficRemaining = computed(() => Math.max(0, (trafficAnalysis.value?.limit_bytes || 0) - (trafficAnalysis.value?.usage?.total || 0)))
const trafficPercent = computed(() => pct(trafficAnalysis.value?.usage?.total || 0, trafficAnalysis.value?.limit_bytes || 0))
const trafficIncomplete = computed(() => {
  const a = trafficAnalysis.value
  return !!a && !a.usage?.calibrated && a.usage?.sample_count > 0 && a.usage.coverage_start > a.cycle_start + 3600
})
function trafficModeLabel(mode:string){ return ({sum:'IN + OUT',max:'IN / OUT 取大',rx:'仅 IN',tx:'仅 OUT'} as Record<string,string>)[mode] || 'IN + OUT' }
function sourcePercent(total:number){ const all=trafficAnalysis.value?.attribution?.total||0; return all ? Math.max(2, Math.min(100, total/all*100)) : 0 }
async function openTrafficAnalysis(s:any){
  showTraffic.value = true; trafficLoading.value = true; trafficAnalysis.value = null
  try{
    trafficAnalysis.value = await apiGet(`/api/admin/monitor/servers/${s.id}/traffic-analysis`)
    await nextTick(); renderTrafficChart()
  }catch(e:any){ message.error(e.message) }
  finally{ trafficLoading.value = false }
}
function renderTrafficChart(){
  if(!trafficChartEl.value || !trafficAnalysis.value?.daily?.length) return
  if(!trafficChart.value) trafficChart.value = echarts.init(trafficChartEl.value)
  const rows=trafficAnalysis.value.daily
  trafficChart.value.setOption({
    animationDuration:500,
    tooltip:{trigger:'axis',valueFormatter:(v:any)=>fmtBytes(Number(v)||0)},
    legend:{top:0,data:['IN','OUT','计费量'],textStyle:{color:'#6b7785',fontSize:11}},
    grid:{left:12,right:12,top:38,bottom:8,containLabel:true},
    xAxis:{type:'category',data:rows.map((d:any)=>d.date.slice(5)),axisTick:{show:false},axisLine:{lineStyle:{color:'#dfe4ea'}},axisLabel:{color:'#7b8794'}},
    yAxis:{type:'value',axisLabel:{formatter:(v:number)=>fmtBytes(v)},splitLine:{lineStyle:{color:'rgba(80,100,120,.09)'}}},
    series:[
      {name:'IN',type:'bar',stack:'physical',data:rows.map((d:any)=>d.rx),itemStyle:{color:'#72a7c7',borderRadius:[3,3,0,0]}},
      {name:'OUT',type:'bar',stack:'physical',data:rows.map((d:any)=>d.tx),itemStyle:{color:'#7fb69b',borderRadius:[3,3,0,0]}},
      {name:'计费量',type:'line',data:rows.map((d:any)=>d.total),smooth:.28,symbolSize:6,lineStyle:{width:2,color:'#b38439'},itemStyle:{color:'#b38439'}},
    ],
  },true)
  trafficChart.value.resize()
}
function disposeTrafficChart(){ trafficChart.value?.dispose(); trafficChart.value=null; trafficAnalysis.value=null }

// ---- 节点 sing-box 版本 ----
const versions = ref<any[]>([])
const minSupported = ref('1.12.0')
const visionFixedVersion = ref('')
const transportFixedVersion = ref('')
const currentReleaseVersion = ref('')
const verLoading = ref(false)
const showVerHelp = ref(false)

function shortVer(v?: string): string {
  if (!v) return '—'
  const idx = v.indexOf('+')
  if (idx < 0) return v
  return v.slice(0, idx) + '+qz'
}
function verBuildMeta(v?: string): string {
  if (!v) return ''
  const idx = v.indexOf('+')
  if (idx < 0) return ''
  return v.slice(idx + 1).replace(/^qz-/, '')
}
function serverVerOf(id: number): string {
  const row = versions.value.find(v => v.server_id === id)
  return row?.version || ''
}
function serverMoreOpts(s: any) {
  return [
    { label: '重建并推送配置', key: 'rebuild' },
    { label: '清除 SSH 主机密钥指纹', key: 'clearKey' },
    { type: 'divider', key: 'd1' },
    { label: () => h('span', { style: 'color:var(--danger)' }, '删除服务器'), key: 'delete' },
  ]
}
function onServerMore(key: string, s: any) {
  if (key === 'rebuild') handleRebuild(s.id)
  else if (key === 'clearKey') handleClearHostKey(s)
  else if (key === 'delete') handleDelete(s.id)
}

async function loadVersions(){
  try{
    const d = await apiGet<any>('/api/admin/nodes/singbox')
    versions.value = d?.nodes || []
    if (d?.min_supported) minSupported.value = d.min_supported
    visionFixedVersion.value = d?.vision_fixed_version || ''
    transportFixedVersion.value = d?.transport_fixed_version || ''
    currentReleaseVersion.value = d?.current_release_version || d?.trojan_fixed_version || ''
  }catch(e:any){ message.error(e?.message || '读取节点版本失败') }
}

async function refreshVersions(){
  verLoading.value = true
  try{
    await apiPost('/api/admin/nodes/singbox/refresh')
    // 探测是后台跑的（不可达的机器要等 SSH 超时），稍等再读一次结果。
    await new Promise(r => setTimeout(r, 2500))
    await loadVersions()
  }catch(e:any){ message.error(e?.message || '检测失败') }
  finally{ verLoading.value = false }
}

function confirmUpgrade(n:any){
  dialog.warning({
    title: '确认重装 sing-box',
    content: `将在「${n.name}」上安装面板发布的 sing-box（随面板版本走）并重启服务。`
      + '安装期间这台机器上的用户会断线，客户端会自动重连。继续？',
    positiveText: '开始安装',
    negativeText: '取消',
    onPositiveClick: () => { doUpgrade(n) },
  })
}

// 安装在后端后台跑：往节点拉 ~60MB 的二进制，同步等会被服务端 WriteTimeout 掐断，
// 于是装成功了前端却报错。这里只负责发起 + 轮询节点列表里的任务状态。
async function doUpgrade(n:any){
  try{
    await apiPost(`/api/admin/nodes/${n.server_id}/singbox/upgrade`)
  }catch(e:any){ message.error(e?.message || '安装启动失败', { duration: 10000 }); return }
  message.info('已开始安装，完成后本页会自动刷新')
  await loadVersions()
  pollUpgrade(n.server_id)
}

// 轮询到该节点的任务结束为止。设上限，免得后端异常时无限轮询。
async function pollUpgrade(id:number){
  for (let i = 0; i < 120; i++){
    await new Promise(r => setTimeout(r, 5000))
    await loadVersions()
    const row = versions.value.find(v => v.server_id === id)
    if (!row || row.upgrading) continue
    if (row.upgrade_error) message.error('安装失败，详情见下方输出', { duration: 10000 })
    else message.success(`安装完成${row.version ? '：' + row.version : ''}`)
    return
  }
}

// ---- Cloudflare 子域名管理 ----
const showDns = ref(false)
const dnsServer = ref<any>(null)
const cfZones = ref<any[]>([])
const cfZonesLoading = ref(false)
const cfZonesLoaded = ref(false)
const cfConfigured = ref(false)
const cfZonesError = ref('')
const dnsSaving = ref(false)
const dnsDeleting = ref(false)
const dnsResult = ref<any>(null)
const dnsForm = reactive({ prefix: '', zone: '', domain: '', domain_v6: '', sync_v4: true, sync_v6: false, cleanup_old: true })
const cfZoneOptions = computed(() => cfZones.value.map((z: any) => ({ label: z.name, value: z.name })))
const dnsHasIPv4 = computed(() => /^(\d{1,3}\.){3}\d{1,3}$/.test((dnsServer.value?.host || '').trim()))
const dnsHasIPv6 = computed(() => /:/.test((dnsServer.value?.host_v6 || '').trim()))

function suggestPrefix(s: any): string {
  const raw = String(s?.name || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
  return raw ? raw.slice(0, 24) : `node-${s?.id || 1}`
}

function syncPrefixToDomain() {
  const p = dnsForm.prefix.trim().toLowerCase().replace(/[^a-z0-9-]/g, '')
  const z = dnsForm.zone.trim().toLowerCase()
  if (!p || !z) return
  dnsForm.domain = `${p}.${z}`
  if (dnsHasIPv6.value) dnsForm.domain_v6 = `${p}-v6.${z}`
}

async function openDnsModal(s: any) {
  dnsServer.value = s
  dnsResult.value = null
  cfZonesError.value = ''
  dnsForm.domain = s.domain || ''
  dnsForm.domain_v6 = s.domain_v6 || ''
  dnsForm.sync_v4 = /^(\d{1,3}\.){3}\d{1,3}$/.test((s.host || '').trim())
  dnsForm.sync_v6 = /:/.test((s.host_v6 || '').trim())
  dnsForm.cleanup_old = true
  dnsForm.prefix = ''
  dnsForm.zone = ''
  showDns.value = true
  cfZonesLoading.value = true
  try {
    const r: any = await apiGet('/api/admin/cf/zones')
    cfConfigured.value = !!r?.configured
    cfZones.value = r?.zones || []
    cfZonesLoaded.value = true
    if (cfZones.value.length) {
      if (dnsForm.domain) {
        const matched = cfZones.value.find((z: any) => dnsForm.domain.endsWith('.' + z.name))
        if (matched) {
          dnsForm.zone = matched.name
          dnsForm.prefix = dnsForm.domain.slice(0, -(matched.name.length + 1))
        }
      } else {
        dnsForm.zone = cfZones.value[0].name
        dnsForm.prefix = suggestPrefix(s)
        syncPrefixToDomain()
      }
    }
    if (!cfConfigured.value) {
      dnsForm.sync_v4 = false
      dnsForm.sync_v6 = false
    }
  } catch (e: any) {
    cfZonesLoaded.value = true
    cfConfigured.value = true
    cfZonesError.value = e?.message || '未知错误'
  } finally {
    cfZonesLoading.value = false
  }
}

async function submitDns(overwrite: boolean) {
  if (!dnsServer.value) return
  dnsSaving.value = true
  try {
    const res: any = await apiPost(`/api/admin/servers/${dnsServer.value.id}/dns`, {
      domain: dnsForm.domain.trim(),
      domain_v6: dnsForm.domain_v6.trim(),
      sync_v4: dnsForm.sync_v4,
      sync_v6: dnsForm.sync_v6,
      overwrite,
      cleanup_old: dnsForm.cleanup_old,
    })
    dnsResult.value = res
    if (res?.server) dnsServer.value = res.server
    message.success('子域名已保存并同步')
    await load()
  } catch (e: any) {
    const msg = e?.message || '保存失败'
    if (!overwrite && (e?.status === 409 || msg.includes('已存在'))) {
      dialog.warning({
        title: 'Cloudflare 已存在同名解析记录',
        content: `${msg}\n\n确认要将该记录改为指向本服务器（灰云仅 DNS）吗？`,
        positiveText: '确认覆盖',
        negativeText: '取消',
        onPositiveClick: () => submitDns(true),
      })
      return
    }
    message.error(msg)
  } finally {
    dnsSaving.value = false
  }
}

function confirmClearDns() {
  if (!dnsServer.value) return
  dialog.warning({
    title: '清除该服务器的子域名？',
    content: '将清空本服务器绑定的对外域名（恢复使用 IP），并删除 Cloudflare 上由轻舟创建的对应托管记录（不会删除手动记录）。',
    positiveText: '确认清除',
    negativeText: '取消',
    onPositiveClick: async () => {
      dnsDeleting.value = true
      try {
        await apiDelete(`/api/admin/servers/${dnsServer.value.id}/dns?delete_cf=1`)
        message.success('已清除子域名')
        showDns.value = false
        await load()
      } catch (e: any) {
        message.error(e?.message || '清除失败')
      } finally {
        dnsDeleting.value = false
      }
    },
  })
}

onMounted(() => { load(); loadVersions() })
onUnmounted(() => trafficChart.value?.dispose())
</script>

<style scoped>
.vp-head-left { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.vp-req {
  font-size: 11.5px; font-weight: 550; color: var(--text-3);
  background: var(--bg-soft); border: 1px solid var(--border);
  padding: 1px 8px; border-radius: 999px;
}
.vp-rel {
  font-size: 11.5px; font-weight: 500; color: var(--accent-strong);
  background: var(--accent-soft); padding: 1px 8px; border-radius: 999px;
}
.vp-rel code { font-family: var(--mono); font-weight: 650; }
.nv-help-details {
  margin-bottom: 10px;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--bg-subtle);
  overflow: hidden;
}
.nv-help-summary {
  display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap;
  padding: 8px 12px; font-size: 12px; font-weight: 560; color: var(--text-2);
  cursor: pointer; user-select: none;
}
.nv-help-summary:hover { background: var(--bg-soft); color: var(--text); }
.nv-help-tags { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 11px; color: var(--text-3); font-weight: 400; }
.nv-help-tags code { font-family: var(--mono); background: #fff; border: 1px solid var(--border); padding: 1px 5px; border-radius: 4px; }
.nv-help-box {
  font-size: 12px; color: var(--text-2); line-height: 1.75;
  padding: 10px 14px 12px;
  background: var(--bg-soft); border-top: 1px solid var(--border);
}
.nv-table { display: flex; flex-direction: column; }
.nv-row {
  display: grid;
  grid-template-columns: minmax(180px, 1.3fr) minmax(140px, 1fr) minmax(240px, 1.6fr) auto;
  align-items: center;
  gap: 10px 14px;
  padding: 10px 4px;
  border-bottom: 1px solid var(--border);
}
.nv-row:last-child { border-bottom: 0; }
.nv-col-node { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.nv-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: #cbd5e1; }
.nv-dot.ok { background: var(--success); box-shadow: 0 0 0 3px rgba(5, 150, 105, .14); }
.nv-dot.err { background: var(--danger); box-shadow: 0 0 0 3px rgba(220, 38, 38, .15); }
.nv-name { font-weight: 650; font-size: 13px; color: var(--text); }
.nv-host {
  font-family: var(--mono); color: var(--text-3); font-size: 11.5px;
  background: var(--bg-soft); padding: 1px 6px; border-radius: 5px;
}
.nv-col-ver { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.nv-ver-badge {
  font-family: var(--mono); font-size: 12.5px; font-weight: 650; color: var(--text);
}
.nv-ver-sub {
  font-family: var(--mono); font-size: 10.5px; color: var(--text-3);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.nv-col-caps { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.cap-pill {
  display: inline-flex; align-items: center; gap: 3px;
  padding: 2px 8px; border-radius: 6px;
  font-size: 11.5px; font-weight: 560;
  color: var(--text-3); background: var(--bg-soft); border: 1px solid transparent;
}
.cap-pill.on {
  color: var(--accent-strong); background: var(--accent-soft);
  border-color: rgba(15, 108, 189, .14);
}
.nv-col-side { display: flex; align-items: center; justify-content: flex-end; gap: 10px; white-space: nowrap; }
.nv-time { font-size: 11px; color: var(--text-3); font-variant-numeric: tabular-nums; }
.nv-err { grid-column: 1 / -1; font-size: 11.5px; line-height: 1.65; color: var(--warn); padding-left: 16px; }
@media (max-width: 960px) {
  .nv-row { grid-template-columns: 1fr auto; }
  .nv-col-ver, .nv-col-caps { grid-column: 1 / -1; padding-left: 16px; }
}
/* 脚本输出可能很长，限高 + 可滚动，免得一次失败把整页撑开。 */
.nv-out {
  margin:4px 0 0; padding:8px 10px; max-height:220px; overflow:auto;
  font-family:var(--mono); font-size:11px; line-height:1.6;
  white-space:pre-wrap; word-break:break-all;
  background:var(--bg-soft); border:1px solid var(--border); border-radius:6px; color:var(--text-2);
}

/* 服务器卡片网格 */
.srv-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
  gap: 14px;
}
@media (max-width: 640px) {
  .srv-grid { grid-template-columns: 1fr; }
}
.srv-item-card {
  display: flex; flex-direction: column; gap: 12px;
  padding: 16px 18px 14px;
  background: var(--card); border: 1px solid var(--border);
  border-radius: var(--r); box-shadow: var(--shadow-sm);
  transition: box-shadow .25s var(--ease-standard), border-color .22s var(--ease-standard), transform .25s var(--ease-emphasized);
}
.srv-item-card:hover {
  box-shadow: var(--shadow); border-color: var(--border-strong); transform: translateY(-2px);
}
.srv-item-card.is-disabled { opacity: .68; }
.sic-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.sic-title-row { display: flex; align-items: center; gap: 8px; min-width: 0; }
.sic-name { font-size: 15px; font-weight: 700; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sic-order { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.order-no {
  display: inline-grid; place-items: center; width: 22px; height: 22px;
  border-radius: 6px; background: var(--bg-soft); border: 1px solid var(--border);
  color: var(--text-2); font-size: 11px; font-weight: 650; font-style: normal; font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}
.sic-info-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 12px;
  padding: 10px 12px;
  background: var(--bg-soft);
  border: 1px solid rgba(15, 23, 42, .055);
  border-radius: 10px;
}
.sic-info-cell { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.sic-info-cell .k { font-size: 11px; color: var(--text-3); }
.sic-info-cell .v { font-size: 12.5px; font-weight: 600; color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sic-info-cell code.v { font-family: var(--mono); font-size: 12px; }
.sic-info-cell .v.muted { color: var(--text-3); font-weight: 400; font-size: 12px; }
.sic-info-cell .v.domain-chip {
  font-family: var(--mono); font-size: 12px; color: var(--success);
}
.tags-inline { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.ver-mini-tag {
  font-family: var(--mono); font-size: 11px; font-weight: 600;
  color: var(--text-2); background: #fff; border: 1px solid var(--border);
  padding: 1px 6px; border-radius: 5px;
}
.sic-foot {
  display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  margin-top: auto; padding-top: 8px; border-top: 1px solid rgba(15, 23, 42, .055);
}

.hint{ font-size:12px; line-height:1.6; color:var(--text-3); margin-top:6px; }
.traffic-summary-grid { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:10px; }
.traffic-summary-card { min-width:0; padding:13px 14px; border:1px solid var(--border); border-radius:12px; background:var(--bg-soft); }
.traffic-summary-card span,.traffic-summary-card small { display:block; color:var(--text-3); font-size:10px; }
.traffic-summary-card b { display:block; margin:4px 0 2px; color:var(--text); font-size:18px; font-variant-numeric:tabular-nums; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.traffic-summary-card.capacity { background:linear-gradient(145deg,rgba(91,145,112,.12),rgba(91,145,112,.04)); }
.quota-overview { margin-top:12px; padding:13px 14px; border:1px solid var(--border); border-radius:12px; }
.quota-overview>div { display:flex; justify-content:space-between; margin-bottom:7px; font-size:12px; }
.quota-overview p { margin:7px 0 0; color:var(--text-3); font-size:11px; line-height:1.6; }
.traffic-section { margin-top:18px; padding-top:16px; border-top:1px solid var(--border); }
.traffic-section-head { display:flex; align-items:flex-start; justify-content:space-between; gap:10px; }
.traffic-section h3 { margin:0; font-size:14px; }
.traffic-section-head p { margin:3px 0 0; color:var(--text-3); font-size:11px; }
.traffic-chart { width:100%; height:270px; margin-top:10px; }
.coverage-chip { padding:3px 7px; border-radius:6px; background:var(--bg-soft); color:var(--text-3); font-size:10px; white-space:nowrap; }
.source-overview { display:flex; flex-wrap:wrap; gap:8px 18px; margin:12px 0; padding:10px 12px; border-radius:10px; background:var(--bg-soft); color:var(--text-3); font-size:11px; }
.source-overview b { color:var(--text); }
.traffic-sources { display:flex; flex-direction:column; gap:10px; }
.traffic-source-row { display:flex; align-items:flex-start; gap:9px; }
.source-rank { display:grid; place-items:center; width:22px; height:22px; border-radius:7px; background:var(--bg-soft); color:var(--text-3); font-size:10px; }
.source-main { flex:1; min-width:0; }
.source-main>div:first-child { display:flex; justify-content:space-between; gap:12px; font-size:12px; }
.source-main>div:first-child span { color:var(--text-2); font-variant-numeric:tabular-nums; }
.source-track { height:5px; margin:5px 0; border-radius:4px; overflow:hidden; background:var(--bg); }
.source-track i { display:block; height:100%; border-radius:inherit; background:linear-gradient(90deg,#6e9fbe,#75aa8f); }
.source-main small { color:var(--text-3); font-size:10px; }
@media(max-width:700px){ .traffic-summary-grid{grid-template-columns:repeat(2,minmax(0,1fr));} .traffic-chart{height:230px;} }
.form-tip { margin-top: 6px; font-size: 12px; color: var(--text-3); line-height: 1.5; }
</style>
