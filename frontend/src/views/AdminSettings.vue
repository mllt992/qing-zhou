<template>
  <div>
    <div class="settings-hero">
      <div>
        <h2 class="page-title">系统设置</h2>
        <p class="page-sub">按分区管理站点、通知、节点与运维配置</p>
      </div>
      <n-input v-model:value="settingsSearch" class="settings-search" clearable
               placeholder="搜索设置，例如 Telegram、退款、证书"
               :input-props="{ 'aria-label': '搜索设置' }" @keydown.enter="openFirstSearchResult" />
    </div>

    <div class="settings-layout">
      <aside class="settings-nav" aria-label="设置分区导航">
        <template v-if="settingsSearch.trim()">
          <div class="settings-nav-group">搜索结果</div>
          <button v-for="section in filteredSettingsSections" :key="section.id" type="button"
                  :class="{ active: activeSectionId === section.id }" @click="selectSettingsSection(section.id, true)">
            <span>{{ section.label }}</span><small>{{ section.note }}</small>
          </button>
          <div v-if="!filteredSettingsSections.length" class="settings-search-empty">没有匹配的设置</div>
        </template>
        <template v-else v-for="group in settingsGroups" :key="group.label">
          <div class="settings-nav-group">{{ group.label }}</div>
          <button v-for="section in group.sections" :key="section.id" type="button"
                  :class="{ active: activeSectionId === section.id }"
                  :aria-current="activeSectionId === section.id ? 'page' : undefined"
                  @click="selectSettingsSection(section.id)">
            <span>{{ section.label }}</span><small>{{ section.note }}</small>
          </button>
        </template>
      </aside>
      <main class="settings-main">
      <div v-if="activeSection" class="settings-section-head">
        <div class="settings-section-group">{{ activeSectionGroup }}</div>
        <h3>{{ activeSection.label }}</h3>
        <p>{{ activeSection.description }}</p>
      </div>
      <n-alert v-if="loadError" type="error" :show-icon="true" class="settings-load-error">
        <template #header>系统配置读取失败，未对数据库做任何修改</template>
        {{ loadError }}。为防止空表单覆盖原配置，“保存设置”已禁用。请稍候重试；如果持续失败，请检查服务日志和数据库路径。
        <n-button size="small" :loading="loading" class="settings-retry" @click="loadSettings">重新读取</n-button>
      </n-alert>
    <OAuth2Settings v-show="activeSectionId === 'settings-oauth'" />
    <n-spin :show="loading">
      <n-card v-show="activeSectionId === 'settings-basic'" id="settings-basic" class="settings-section" size="small">
        <n-form label-placement="top">
          <n-form-item label="站点名称"><n-input v-model:value="form.site_name" /></n-form-item>
          <n-form-item label="站点描述"><n-input v-model:value="form.site_description" /></n-form-item>
          <n-form-item label="注册模式">
            <n-select v-model:value="form.register_mode" :options="[{label:'开放注册',value:'open'},{label:'邀请码注册',value:'code'},{label:'关闭注册',value:'closed'}]" />
          </n-form-item>
          <n-form-item label="邮箱验证">
            <div>
              <n-switch v-model:value="emailVerify" />
              <div style="font-size:12px;color:var(--text-3);line-height:1.7;margin-top:4px;max-width:520px;">
                开放注册的新用户未验证邮箱时，订阅里不会下发节点。用积分购买或管理员分配套餐后即可使用对应节点。邀请码注册和管理员开户不受影响。
              </div>
            </div>
          </n-form-item>
          <n-form-item label="积分汇率（积分=1元）"><n-input-number v-model:value="pointsRate" :min="1" style="width:200px;" /></n-form-item>
          <n-form-item label="注册赠送积分"><n-input-number v-model:value="signupBonus" :min="0" style="width:200px;" /></n-form-item>
          <n-form-item label="新用户默认流量 (GB)"><n-input-number v-model:value="defaultTraffic" :min="0" style="width:200px;" /></n-form-item>
          <n-form-item label="新用户默认天数"><n-input-number v-model:value="defaultExpiry" :min="0" style="width:200px;" /></n-form-item>
          <n-form-item label="免费节点分组">
            <n-select v-model:value="freeGroupId" :options="groupOptions" placeholder="无计划用户可用的节点分组" clearable style="width:300px;" />
          </n-form-item>
          <n-form-item label="用户自助重置凭据">
            <div>
              <n-switch v-model:value="credsResetEnabled" />
              <div style="font-size:12px;color:var(--text-3);line-height:1.7;margin-top:4px;max-width:520px;">
                允许用户在「我的订阅」页自行重置节点凭据，彻底吊销已泄露订阅导出的节点（每人 30 天一次）。
                每次重置都需要把新凭据推送到相关服务器并重启 sing-box，<b>期间同机器上其他用户的连接也会中断</b>，
                因此默认关闭。关闭时管理员仍可在「用户管理」里逐个重置。
              </div>
            </div>
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-access'" id="settings-access" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:12px;line-height:1.7;">
          面板对外访问地址，用于订阅链接、探针安装、邮件验证/找回链接，以及下方的 sing-box 一键安装命令。
          填写完整地址，例如 <code>https://node.example.com</code> 或 <code>http://1.2.3.4:8081</code>；
          不带 <code>http(s)://</code> 前缀时默认按 <code>https</code> 处理。留空则自动依据反向代理头 / 请求 Host 推断。
        </p>
        <n-form label-placement="top">
          <n-form-item label="访问地址">
            <n-input v-model:value="form.public_base" :disabled="envLocked('public_base')"
              placeholder="https://node.example.com 或 http://1.2.3.4:8081" style="max-width:420px;" />
          </n-form-item>
          <n-form-item v-if="envLocked('public_base')" label="配置来源">
            <span style="font-size:12px;color:var(--warning,#d97706);">
              已由环境变量 QZ_PUBLIC_BASE 固定；如需在面板内修改，请移除该环境变量后重启。
            </span>
          </n-form-item>
          <!-- 和上面那栏长得像，但不是一回事：上面是「客户端去哪儿取订阅」，这里是
               「订阅里的节点连到哪儿」。留空且一台服务器都没加过时，自建节点不会
               出现在任何人的订阅里 —— 节点建好了、套餐也生效，订阅却是空的。 -->
          <n-form-item label="节点对外地址">
            <div style="width:100%;max-width:560px;">
              <n-input-group style="max-width:420px;">
                <n-input v-model:value="form.node_host_override"
                  placeholder="留空 = 用第一台已启用服务器的地址" />
                <n-button ghost :loading="detecting" @click="detectNodeHost">自动获取</n-button>
              </n-input-group>
              <!-- 探测只给候选、不直接落库：填错的代价不对等。留空的表现是「共 0 个
                   节点」，一眼看得出；静默填错（比如橙云代理的域名）变成「订阅里
                   有链接但连不上」，排查成本高得多。所以来源要摆在眼前让人挑。 -->
              <div v-if="hostCandidates.length" class="host-cands">
                <div class="host-cands-t">点一条填入：</div>
                <button v-for="c in hostCandidates" :key="c.value" type="button"
                  class="host-cand" @click="pickNodeHost(c)">
                  <span class="host-cand-v">{{ c.value }}</span>
                  <span class="host-cand-l">{{ c.label }}<template v-if="c.recommended"> · 推荐</template></span>
                  <span class="host-cand-n">{{ c.note }}</span>
                </button>
              </div>
              <p style="font-size:12px;color:var(--text-3);margin-top:6px;line-height:1.7;">
                写进订阅里的节点地址（只填域名或 IP，不带端口和 <code>http://</code>）。留空时自动取第一台已启用「服务器」的地址。
                <b>节点跑在面板本机时必须填这里</b> —— 本机不是一条「服务器」记录（它不需要 SSH 下发），
                自动取值取不到，订阅会是空的：节点列表显示「共 0 个节点」。
                <br>面板挂在 Cloudflare 橙云等反代后面时，这里要填<b>源站 IP</b>，不能填被代理的域名。
              </p>
            </div>
          </n-form-item>
          <n-form-item label="sing-box 安装命令">
            <div style="width:100%;max-width:560px;">
              <n-input-group>
                <n-input :value="installCmd" readonly style="font-family:monospace;font-size:12px;" />
                <n-button type="primary" ghost @click="copyInstall">复制</n-button>
              </n-input-group>
              <p style="font-size:12px;color:var(--text-3);margin-top:6px;line-height:1.7;">
                在落地服务器上以 root 运行此命令：已安装 sing-box 会自动检测并打印信息；未安装则拉取官方最新版（含
                v2ray_api，面板统计依赖）并完成内核调优，最后输出可填入「服务器」的接管信息。
              </p>
            </div>
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-refund'" id="settings-refund" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:12px;line-height:1.7;">
          管理员对订单退款时的默认规则。<b>按剩余比例</b>只退还未使用的部分（如 100G 用了 50G 退 50%）；
          套餐同时含流量与有效期，<b>计算基准</b>决定按哪个维度算比例，推荐 <b>min(流量,时间)</b> 取更小值以防滥用。
          流量包无有效期，恒按流量。下架商品时也按此策略退款。
        </p>
        <n-form label-placement="top">
          <n-form-item label="退款方式">
            <n-select v-model:value="refundMode" style="width:240px;" :options="[
              {label:'按剩余比例退款',value:'prorated'},
              {label:'全额退款',value:'full'},
            ]" />
          </n-form-item>
          <n-form-item label="套餐计算基准">
            <n-select v-model:value="refundBasis" :disabled="refundMode==='full'" style="width:240px;" :options="[
              {label:'min(流量, 时间) 取更小（推荐）',value:'min'},
              {label:'只按剩余流量',value:'traffic'},
              {label:'只按剩余时间',value:'time'},
            ]" />
          </n-form-item>
          <n-form-item label="手续费 (%)">
            <n-input-number v-model:value="refundFee" :disabled="refundMode==='full'" :min="0" :max="100" style="width:200px;" />
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-home'" id="settings-home" class="settings-section" size="small">
        <n-form label-placement="top">
          <n-form-item label="首页模式">
            <n-select v-model:value="form.homepage_mode" :options="[{label:'监控大屏',value:'monitor'},{label:'自定义页面',value:'custom'}]" />
          </n-form-item>
          <n-form-item v-if="form.homepage_mode === 'custom'" label="自定义 URL">
            <n-input v-model:value="form.homepage_url" placeholder="https://example.com" />
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-help'" id="settings-help" class="settings-section" size="small">
        <p class="section-intro">
          选择用户点击「帮助中心」时使用站内 Markdown 文档，还是跳转到独立部署的外部帮助站点。
          后台「帮助文档」管理始终保留，切回站内模式后可继续使用原有内容。
        </p>
        <n-form label-placement="top">
          <n-form-item label="帮助文档来源">
            <n-select v-model:value="form.help_docs_mode" style="width:260px;" :options="[
              {label:'内置帮助中心',value:'builtin'},
              {label:'外部帮助文档',value:'external'},
            ]" />
          </n-form-item>
          <n-form-item v-if="form.help_docs_mode === 'external'" label="外部文档 URL">
            <div style="width:100%;max-width:560px;">
              <n-input v-model:value="form.help_docs_url" placeholder="https://docs.example.com" />
              <p style="font-size:12px;color:var(--text-3);margin-top:6px;line-height:1.7;">
                必须填写完整的 <code>http://</code> 或 <code>https://</code> 地址。用户从菜单或控制台点击时会在新标签页打开。
              </p>
            </div>
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-smtp'" id="settings-smtp" class="settings-section" size="small">
        <!-- 没配 SMTP 时，依赖邮件的功能会安静地失效：面板日志里有链接，用户那边
             什么都收不到。把后果写出来，而不是留一组空输入框让人以为「可选」。 -->
        <div v-if="!smtpConfigured" class="warn-box">
          <b>当前未配置邮件服务</b>，以下功能不可用：
          <ul>
            <li>用户「找回密码」——登录框里会直接提示去找管理员，重置只能你在「用户管理 → 编辑 → 重置密码」里做。</li>
            <li v-if="emailVerify">开放注册后的邮箱验证——<b>「基本设置」里的「邮箱验证」是开着的，开放注册的新用户不点邮件就拿不到免费节点</b>。用积分购买或管理员分配套餐后仍可使用对应节点。邀请码注册和管理员开户不受影响。请配好 SMTP，或关掉它。</li>
            <li v-else>开放注册后的邮箱验证（当前「邮箱验证」是关的，不影响注册）。</li>
          </ul>
        </div>
        <n-form label-placement="top">
          <n-form-item label="SMTP 主机"><n-input v-model:value="form.smtp_host" /></n-form-item>
          <n-form-item label="SMTP 端口"><n-input v-model:value="form.smtp_port" /></n-form-item>
          <n-form-item label="加密方式">
            <n-select v-model:value="form.smtp_security" :options="[{label:'自动',value:'auto'},{label:'SSL/TLS',value:'ssl'},{label:'STARTTLS',value:'starttls'},{label:'无',value:'none'}]" />
          </n-form-item>
          <n-form-item label="SMTP 用户"><n-input v-model:value="form.smtp_user" /></n-form-item>
          <n-form-item label="SMTP 密码"><n-input v-model:value="form.smtp_pass" type="password" show-password-on="click" /></n-form-item>
          <n-form-item label="发件人地址"><n-input v-model:value="form.smtp_from" /></n-form-item>
          <n-form-item label="发件人名称"><n-input v-model:value="form.smtp_from_name" /></n-form-item>
          <n-form-item label="测试收件人">
            <n-input v-model:value="testEmail" placeholder="发送测试邮件的目标地址" style="width:260px;" />
            <n-button style="margin-left:8px;" :loading="testingSmtp" @click="handleTestSMTP">发送测试</n-button>
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-telegram'" id="settings-telegram" class="settings-section" size="small">
        <div class="tg-subnav" role="tablist" aria-label="Telegram 设置分区">
          <button v-for="panel in telegramPanels" :key="panel.id" type="button" role="tab"
                  :class="{ active: telegramPanel === panel.id }"
                  :aria-selected="telegramPanel === panel.id" @click="telegramPanel = panel.id">
            {{ panel.label }}
          </button>
        </div>

        <div v-show="telegramPanel === 'bot'" class="tg-panel" role="tabpanel">
          <div v-if="!telegramConfigured" class="warn-box">
            <b>当前未配置 Telegram Bot</b>。配好后，用户可在「账户设置」里绑定，用聊天查询订阅 / 套餐 / 流量，并接收到期和流量不足通知。
          </div>
          <p class="section-intro">
            在 <a href="https://t.me/BotFather" target="_blank" rel="noopener">@BotFather</a> 创建机器人，把 Token 贴到下面。
            面板用长轮询收消息，不需要公网 Webhook。Token 加密存储；清空并保存即关闭 Bot。
            订阅地址会发到 Telegram，请提醒用户不要把聊天记录转发出去。
          </p>
          <n-form label-placement="top">
            <n-form-item label="Bot Token">
              <n-input v-model:value="form.telegram_bot_token" type="password" show-password-on="click"
                       :disabled="envLocked('telegram_bot_token')"
                       placeholder="123456:ABC…；显示 *** 表示已设置，清空并保存即关闭" />
            </n-form-item>
            <n-form-item v-if="form.telegram_bot_username" label="Bot 用户名">
              <span>@{{ form.telegram_bot_username }}</span>
            </n-form-item>
            <n-form-item label="连接测试">
              <div class="inline-action">
                <n-button :loading="testingTg" :disabled="!telegramConfigured && !(form.telegram_bot_token || '').trim()"
                          @click="handleTestTelegram">测试连接</n-button>
                <span class="form-hint">会调用 getMe；若你自己已绑定，还会往你的聊天发一条测试消息。</span>
              </div>
            </n-form-item>
          </n-form>
        </div>

        <div v-show="telegramPanel === 'alerts'" class="tg-panel" role="tabpanel">
          <div class="tg-tpl-h">用户提醒规则</div>
          <p class="section-intro">控制套餐到期和流量不足通知的触发时机，修改后需保存设置。</p>
          <n-form label-placement="top">
            <n-form-item label="到期提前提醒">
              <div class="inline-field">
                <n-input-number v-model:value="notifyExpiryDays" :min="1" :max="30" style="width:160px;" />
                <span class="form-hint">天（1–30）</span>
              </div>
            </n-form-item>
            <n-form-item label="流量不足阈值">
              <div class="inline-field">
                <n-input-number v-model:value="notifyTrafficPct" :min="1" :max="50" style="width:160px;" />
                <span class="form-hint">% 剩余（1–50）</span>
              </div>
            </n-form-item>
          </n-form>
        </div>

        <div v-show="telegramPanel === 'commands'" class="tg-panel" role="tabpanel">
          <div class="tg-tpl-h">自定义指令</div>
          <p class="form-hint" style="margin:0 0 10px;">
            添加固定回复指令，保存后会同步到 Telegram 的指令菜单。名称不用输入 <code>/</code>；
            回复支持 Telegram HTML，以及 <code v-pre>{{site}}</code>、<code v-pre>{{username}}</code>、
            <code v-pre>{{panel}}</code>、<code v-pre>{{panel_link}}</code> 占位符。未绑定用户的 <code v-pre>{{username}}</code> 为空。
          </p>
          <div v-if="telegramCustomCommands.length" class="tg-custom-list">
            <div v-for="(item, index) in telegramCustomCommands" :key="item.id" class="tg-custom-item">
              <div class="tg-custom-head">
                <n-input v-model:value="item.command" maxlength="32" placeholder="指令，如 contact">
                  <template #prefix>/</template>
                </n-input>
                <n-input v-model:value="item.description" maxlength="256" show-count placeholder="菜单说明，如 联系客服" />
                <n-button quaternary type="error" @click="removeTelegramCustomCommand(index)">删除</n-button>
              </div>
              <n-input v-model:value="item.response" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }"
                       maxlength="4096" show-count placeholder="发送该指令时机器人的回复内容" />
            </div>
          </div>
          <n-button size="small" :disabled="telegramCustomCommands.length >= 20" @click="addTelegramCustomCommand">
            添加自定义指令
          </n-button>
          <span class="form-hint" style="margin-left:8px;">最多 20 条；不能覆盖内置指令。</span>
        </div>

        <div v-show="telegramPanel === 'ops'" class="tg-panel" role="tabpanel">
          <div class="tg-tpl-h">运维保护 · 节点反复重启熔断</div>
          <p class="form-hint" style="margin:0 0 10px;">
            节点每重启一次，它上面所有人的连接都会断一次。改配置引起的重启是正常的，
            这里只统计<b>没有任何后台操作时</b>自动发生的重启：超过阈值后暂停该节点的周期性自动下发，
            在监控页告警并推送给下面选中的接收人。流量统计和探针上报不受影响，人工重新下发成功后自动解除。
          </p>
          <n-form label-placement="top">
            <n-form-item label="启用">
              <n-switch v-model:value="restartAlertOn" />
            </n-form-item>
            <n-form-item label="判定条件">
              <div class="restart-condition">
                <label>
                  <span>统计窗口（分钟）</span>
                  <n-input-number v-model:value="restartWindowMin" :min="5" :max="360" />
                </label>
                <label>
                  <span>触发次数</span>
                  <n-input-number v-model:value="restartCount" :min="2" :max="50" />
                </label>
              </div>
            </n-form-item>
            <n-form-item label="接收人">
              <div class="ops-recipients">
                <div v-if="!opsCandidates.length" class="form-hint">
                  还没有账号绑定 Telegram。接收人不必是管理员 —— 让对方在「账户设置」里绑定后，这里就能勾选。
                </div>
                <template v-else>
                  <div class="ops-recipient-summary">
                    <span class="ops-live-state" :class="`is-${opsSaveState}`">
                      <i></i>{{ opsSaveStatusText }}
                    </span>
                    <span class="ops-selected-count">已选 {{ opsSelectedCount }} 人</span>
                  </div>
                  <div class="ops-recipient-list">
                    <n-checkbox v-for="c in opsCandidates" :key="c.user_id" class="ops-recipient"
                                :class="{ 'ops-recipient--checked': c.on }"
                                :checked="c.on" :disabled="opsUpdating.has(c.user_id)"
                                @update:checked="(v: boolean) => toggleOpsRecipient(c, v)">
                      <span class="ops-recipient-copy">
                        <span class="ops-recipient-name">{{ c.username }}</span>
                        <span class="ops-recipient-meta">
                          <span class="ops-recipient-role">{{ c.is_admin ? '管理员' : '普通用户' }}</span>
                          <span class="ops-recipient-tg">@{{ c.tg_name }}</span>
                        </span>
                      </span>
                    </n-checkbox>
                  </div>
                </template>
                <div class="form-hint ops-recipient-hint">接收人选择会自动保存。告警里会出现节点名和重启次数，请只选择可以查看这些信息的人。</div>
              </div>
            </n-form-item>
            <n-form-item label="额外 chat ID">
              <div class="field-stack">
                <n-input v-model:value="form.alert_ops_extra_chats" type="textarea" :autosize="{ minRows: 1, maxRows: 3 }"
                         placeholder="推到群/频道：把 Bot 拉进去，填该会话的 chat_id，多个用逗号分隔" />
                <span class="form-hint">额外 chat ID 属于普通设置，需要点击页面底部的“保存设置”。</span>
              </div>
            </n-form-item>
            <n-form-item label="发送测试">
              <n-button size="small" :loading="testingOps" :disabled="!opsEffective" @click="handleTestOpsAlert">发送测试告警</n-button>
              <span v-if="opsEffective" class="form-hint" style="margin-left:8px;">当前 {{ opsEffective }} 个聊天会收到告警</span>
              <span v-else style="margin-left:8px;color:#dc2626;font-size:12px;">当前没有人会收到告警</span>
            </n-form-item>
          </n-form>
        </div>

        <div v-show="telegramPanel === 'templates'" class="tg-panel" role="tabpanel">
          <div class="tg-tpl-h">消息排版</div>
          <p class="form-hint" style="margin:0 0 10px;">
            发给用户的查询结果和通知都走模板。支持 Telegram HTML（<code>&lt;b&gt;</code> <code>&lt;i&gt;</code> <code>&lt;code&gt;</code> <code>&lt;a href&gt;</code>）和占位符 <code v-pre>{{name}}</code>。
            留空 / 与内置一致时跟随内置（升级会拿到新排版）；改过之后一直用你的版本。
          </p>
          <n-space align="center" style="margin-bottom:8px;">
            <n-select v-model:value="tgTplKey" :options="tgTplOptions" style="width:220px;" />
            <n-button size="tiny" @click="loadTgTplDefault">载入内置默认</n-button>
            <n-button size="tiny" @click="resetTgTpl">恢复内置默认</n-button>
          </n-space>
          <div class="tg-vars">
            <div class="tg-vars-h">本模板可用占位符 <span>（点击插入）</span></div>
            <div v-if="currentTgVars.length" class="tg-vars-list">
              <button v-for="v in currentTgVars" :key="v.key" type="button" class="tg-var" @click="insertTgVar(v.key)">
                <code>{{ tgToken(v.key) }}</code>
                <span>{{ v.desc }}</span>
              </button>
            </div>
            <p v-else class="form-hint" style="margin:0;">加载模板说明后会显示占位符。</p>
          </div>
          <n-input ref="tgTplInput" v-model:value="currentTgTplBody" type="textarea" :rows="12"
                   placeholder="留空用内置模板" style="font-family:ui-monospace,Consolas,monospace;font-size:12.5px;line-height:1.55;" />
          <div class="tg-preview">
            <div class="tg-preview-h">预览（示例数据）</div>
            <pre class="tg-preview-body">{{ tgTplPreview }}</pre>
          </div>
        </div>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-cert'" id="settings-cert" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:10px;">
          填写 Cloudflare API Token 后，「证书管理」页即可用 Cloudflare DNS 方式在面板本机一键申请 / 自动续期真实证书（DNS 验证无需节点参与，远程节点也能用）。
        </p>
        <details class="cf-guide">
          <summary class="cf-guide-t">如何获取 Cloudflare API Token（约 1 分钟）</summary>
          <ol>
            <li>打开 <a href="https://dash.cloudflare.com/profile/api-tokens" target="_blank" rel="noopener">Cloudflare → 我的个人资料 → API 令牌</a>，点<b>「创建令牌」</b>。</li>
            <li>选用模板 <b>「编辑区域 DNS（Edit zone DNS）」</b> —— 它已自带所需权限。<br>（若手动创建，需添加两条权限：<b>区域 · DNS · 编辑</b> 和 <b>区域 · 区域 · 读取</b>。）</li>
            <li>「区域资源」选 <b>包含 → 特定区域 → 你的域名</b>（或「所有区域」）。</li>
            <li>「继续以显示摘要」→ <b>创建令牌</b> → 复制生成的令牌，粘贴到下方。</li>
          </ol>
          <div class="cf-guide-n">✓ 只需 DNS 编辑权限，<b>不要用 Global API Key</b>（那是全账户权限，不安全）。令牌加密存储，保存后显示为 <code>***</code>。</div>
        </details>
        <n-form label-placement="top">
          <n-form-item label="Cloudflare API Token">
            <n-input v-model:value="form.cf_api_token" type="password" show-password-on="click" placeholder="留空表示未配置；显示 *** 表示已设置" />
          </n-form-item>
          <n-form-item label="ACME 账户邮箱">
            <n-input v-model:value="form.acme_email" placeholder="可选，建议填写（Let's Encrypt 到期提醒）" />
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-security'" id="settings-security" class="settings-section" size="small">
        <n-form label-placement="top">
          <n-form-item label="阻断内网 / 元数据">
            <div style="width:100%;">
              <n-switch v-model:value="blockPrivate" />
              <p style="font-size:12px;color:var(--text-3);margin:6px 0 0;">
                开启后，用户经节点访问 <code>127.0.0.1</code>、内网段（10./172.16./192.168.）、链路本地段（含云厂商元数据地址 <code>169.254.169.254</code>）会被拒绝，只放行公网目标。
                <b>用 IP 或用域名访问都拦得住</b>——节点会先把域名解析成地址再判断，否则随便注册一个指向内网的域名就能绕过去。
                中转入站不在本机做这步解析（那会让链路被就近解析到错误的 CDN 节点），改由落地机按同样规则拦截，防护不打折。
                <b>建议保持开启</b>：关闭意味着任何订阅用户都能借落地机的身份访问它所在的内网，并可能读到该机的云凭据。
                仅当你确实要让用户经节点访问自己的内网时才关闭。保存后会自动下发到所有节点。
              </p>
            </div>
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-template'" id="settings-template" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:12px;">自定义 Clash/sing-box 订阅输出模板。留空使用内置默认模板；改过之后会一直沿用你的版本（升级带来的新版内置模板不会自动生效），点「恢复内置默认」即可清空覆盖、跟随内置。</p>
        <n-form label-placement="top">
          <n-form-item label="Clash 声明 UDP">
            <div style="width:100%;">
              <n-switch :value="form.sub_clash_udp !== '0' && form.sub_clash_udp !== 'false'"
                        @update:value="(v: boolean) => form.sub_clash_udp = v ? '1' : '0'" />
              <div style="font-size:12px;color:var(--text-3);margin-top:6px;line-height:1.6;">
                开启时（默认）Clash 订阅对支持的协议写 <code>udp: true</code>。关闭则省略该字段。入站 egress 阻断 UDP 的节点仍会强制 <code>udp: false</code>。
              </div>
            </div>
          </n-form-item>
          <n-form-item label="Clash 模板 (YAML)">
            <div style="width:100%;">
              <n-input v-model:value="form.sub_clash_template" type="textarea" :rows="8" placeholder="留空用内置模板" style="font-family:monospace;font-size:12px;" />
              <n-space size="small" style="margin-top:6px;">
                <n-button size="tiny" @click="loadDefaultTemplate('clash')">载入内置默认（可编辑）</n-button>
                <n-button size="tiny" @click="form.sub_clash_template = ''">恢复内置默认（清空）</n-button>
              </n-space>
            </div>
          </n-form-item>
          <n-form-item label="sing-box 模板 (JSON)">
            <div style="width:100%;">
              <n-input v-model:value="form.sub_singbox_template" type="textarea" :rows="8" placeholder="留空用内置模板" style="font-family:monospace;font-size:12px;" />
              <n-space size="small" style="margin-top:6px;">
                <n-button size="tiny" @click="loadDefaultTemplate('singbox')">载入内置默认（可编辑）</n-button>
                <n-button size="tiny" @click="form.sub_singbox_template = ''">恢复内置默认（清空）</n-button>
              </n-space>
            </div>
          </n-form-item>
        </n-form>
        <p style="font-size:12px;color:var(--text-3);margin-top:4px;">改动需点下方「保存设置」后生效。</p>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-runtime'" id="settings-runtime" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:12px;line-height:1.7;">
          调大间隔可减少小型 VPS 的探针采样、SSH 握手和健康检查开销。保存后面板调度立即更新；
          已升级的新探针会在下一次上报时自动领取新间隔，无需重启节点服务。
        </p>
        <n-form label-placement="top">
          <n-form-item label="探针采集间隔（秒）">
            <div style="width:100%;">
              <n-input-number v-model:value="probeIntervalSeconds" :min="30" :max="3600" :step="30"
                              :disabled="envLocked('monitor_probe_interval_seconds')" style="width:220px;" />
              <p class="form-hint">范围 30–3600 秒，默认 60 秒。影响节点监控曲线刷新速度，不影响流量计费。</p>
            </div>
          </n-form-item>
          <n-form-item label="流量统计间隔（分钟）">
            <div style="width:100%;">
              <n-input-number v-model:value="statsIntervalMinutes" :min="1" :max="60" :step="1"
                              :disabled="envLocked('singbox_stats_interval_minutes')" style="width:220px;" />
              <p class="form-hint">范围 1–60 分钟，默认 10 分钟。间隔越大，面板用量、超额停用和到期剔除的生效延迟越长。用户「在线」窗口跟着这个间隔走：当前约 {{ userOnlineWindowMinutes }} 分钟（两次轮询加 30 秒）。保存后立即生效，不必重启面板。</p>
            </div>
          </n-form-item>
          <n-form-item label="完整健康检查（分钟）">
            <div style="width:100%;">
              <n-input-number v-model:value="reconcileIntervalMinutes" :min="10" :max="1440" :step="10"
                              :disabled="envLocked('singbox_reconcile_interval_minutes')" style="width:220px;" />
              <p class="form-hint">范围 10–1440 分钟，默认 60 分钟，且不能小于流量统计间隔。检查远端配置文件和 sing-box 服务状态。</p>
            </div>
          </n-form-item>
          <n-alert v-if="runtimeEnvLocked" type="warning" :show-icon="true">
            部分间隔由服务器环境变量固定，面板中只显示实际值。移除对应环境变量并重启面板后才能在线修改。
          </n-alert>
        </n-form>
        <div class="section-operation">
          <div>
            <b>手动重建节点配置</b>
            <p>保存 sing-box 相关设置后，可立即重新生成并下发配置。</p>
          </div>
          <n-button :loading="rebuilding" @click="handleRebuild">重建 sing-box 配置</n-button>
        </div>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-monitor'" id="settings-monitor" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:12px;">超过以下百分比时触发告警（0-100）。修改后下次检查生效。</p>
        <n-form label-placement="top">
          <n-form-item label="CPU 告警 (%)"><n-input-number v-model:value="alertCpu" :min="1" :max="100" style="width:200px;" /></n-form-item>
          <n-form-item label="内存告警 (%)"><n-input-number v-model:value="alertMem" :min="1" :max="100" style="width:200px;" /></n-form-item>
          <n-form-item label="磁盘告警 (%)"><n-input-number v-model:value="alertDisk" :min="1" :max="100" style="width:200px;" /></n-form-item>
          <n-form-item label="连续命中次数">
            <div style="width:100%;">
              <n-input-number v-model:value="alertStreak" :min="1" :max="10" style="width:200px;" />
              <p style="font-size:12px;color:var(--text-3);margin:6px 0 0;">
                CPU / 内存 / 磁盘 / 离线这四项，需要<b>连续命中这么多次检查</b>才真正告警。
                一次编译、一次备份就能让 CPU 瞬间冲顶，只看单次采样会把告警刷成噪音。
                填 <code>1</code> 恢复「采到一次就报」。到期类告警不受影响（日期不会抖动）。
              </p>
            </div>
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-update'" id="settings-update" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:10px;">
          「在线更新」页查版本走的是 GitHub 公开接口，匿名调用<b>按出口 IP</b> 限额（每小时 60 次）。
          与别人共用一个出口 IP（NAT / 机房 / 公司网络）时很容易撞到额度，表现为检查更新报「速率受限」。
          填一个 GitHub Token 后额度提到每小时 5000 次。<b>只是提额度，不是权限</b>——
          仓库是公开的，令牌<b>不需要勾任何权限范围（scope）</b>，建一个空权限的即可。
        </p>
        <n-form label-placement="top">
          <n-form-item label="GitHub Token">
            <div style="width:100%;">
              <n-input v-model:value="form.update_github_token" type="password" show-password-on="click"
                       :disabled="envLocked('update_github_token')"
                       placeholder="可选；显示 *** 表示已设置，清空并保存即移除" />
              <div class="form-hint">
                <template v-if="envLocked('update_github_token')">
                  当前由环境变量 <code>QZ_UPDATE_GITHUB_TOKEN</code> 指定，面板改不动。
                </template>
                <template v-else>
                  在 <a href="https://github.com/settings/tokens" target="_blank" rel="noopener">GitHub → Settings → Developer settings → Personal access tokens</a>
                  生成，不勾任何 scope。加密存储，保存后显示为 <code>***</code>；
                  填错了就把这一栏清空再保存，即可退回匿名调用。
                </template>
              </div>
            </div>
          </n-form-item>
        </n-form>
      </n-card>

      <n-card v-show="activeSectionId === 'settings-backup'" id="settings-backup" class="settings-section" size="small">
        <p style="font-size:12px;color:var(--text-3);margin-bottom:10px;">
          在线导出整库快照（单个 <code>.db</code> 文件，含用户 / 订单 / 节点 / 证书）。数据库跑在 WAL 模式下，
          <b>直接 <code>scp</code> 拷贝 <code>qingzhou.db</code> 拿到的是残缺副本</b>——已提交的数据可能还在 <code>-wal</code> 里。
          此处导出由 SQLite 自己在一致性快照上生成，导出期间面板照常读写。
          文件里的敏感字段仍是加密的，恢复到别处需要同一个 <code>QZ_SECRET_KEY</code>。
        </p>
        <n-button :loading="backingUp" @click="handleBackup">下载数据库备份</n-button>
      </n-card>

      <div v-if="hasUnsavedChanges || saving" class="settings-actions">
        <div class="settings-dirty-copy" role="status" aria-live="polite">
          <b>{{ saving ? '正在保存…' : `有 ${dirtyCount} 项未保存更改` }}</b>
          <span>保存后统一生效</span>
        </div>
        <n-button :disabled="saving" @click="confirmDiscardChanges">放弃更改</n-button>
        <n-button type="primary" :loading="saving" :disabled="!settingsLoaded" @click="handleSave">保存设置</n-button>
      </div>
    </n-spin>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import OAuth2Settings from '@/components/OAuth2Settings.vue'
import { ref, reactive, computed, nextTick, onMounted, onBeforeUnmount, watch } from 'vue'
import { onBeforeRouteLeave, useRoute, useRouter } from 'vue-router'
import { NAlert, NCard, NCheckbox, NForm, NFormItem, NInput, NInputGroup, NInputNumber, NSelect, NSwitch, NButton, NSpace, NSpin, useDialog, useMessage } from 'naive-ui'
import { apiGet, apiPost, apiPut, apiList, apiDownload } from '@/api'
import { useConfigStore } from '@/stores/config'

const message = useMessage()
const dialog = useDialog()
const route = useRoute()
const router = useRouter()
const config = useConfigStore()
const settingsLoaded = ref(false)
const loadError = ref('')
type SettingsSection = { id: string; label: string; note: string; description: string; keywords: string }
type SettingsGroup = { label: string; sections: SettingsSection[] }
const settingsGroups: SettingsGroup[] = [
  { label: '通用设置', sections: [
    { id: 'settings-oauth', label: 'OAuth2 / OIDC', note: '认证中心登录', description: '配置认证中心，支持单点登录和已有账号绑定。此分区单独保存。', keywords: 'OAuth2 OIDC auth2 认证中心 Client ID Secret SSO' },
    { id: 'settings-basic', label: '基本设置', note: '注册与积分', description: '配置站点信息、注册规则和新用户默认权益。', keywords: '站点名称 描述 注册 邮箱验证 积分 流量 免费节点 凭据' },
    { id: 'settings-access', label: '访问地址', note: '面板与节点', description: '设置面板公开地址、节点连接地址和安装命令。', keywords: '域名 public base 节点 IP sing-box 安装 命令' },
    { id: 'settings-home', label: '首页设置', note: '入口展示', description: '选择访客首页显示监控大屏或自定义页面。', keywords: '首页 监控 自定义 URL' },
    { id: 'settings-help', label: '帮助中心', note: '内置或外部', description: '选择用户使用站内帮助文档或跳转到外部文档站点。', keywords: '帮助 文档 内置 外部 URL 跳转' },
  ] },
  { label: '用户与计费', sections: [
    { id: 'settings-refund', label: '退款策略', note: '比例与手续费', description: '设置管理员退款时采用的比例、计算基准和手续费。', keywords: '订单 退款 流量 时间 手续费' },
  ] },
  { label: '通知与告警', sections: [
    { id: 'settings-smtp', label: 'SMTP 邮件', note: '验证与找回', description: '配置验证邮件、密码找回邮件和发送测试。', keywords: '邮件 SMTP 密码 找回 验证 发件人 TLS SSL' },
    { id: 'settings-telegram', label: 'Telegram', note: '机器人与模板', description: '管理机器人连接、提醒规则、运维接收人和消息模板。', keywords: 'Telegram Bot Token 提醒 接收人 chat ID 指令 消息模板' },
    { id: 'settings-monitor', label: '监控告警', note: '阈值与通知', description: '配置 CPU、内存、磁盘与离线告警的触发阈值。', keywords: 'CPU 内存 磁盘 离线 连续 阈值' },
  ] },
  { label: '节点与安全', sections: [
    { id: 'settings-cert', label: '证书 / ACME', note: 'Cloudflare DNS', description: '配置 Cloudflare DNS 验证所需的令牌与 ACME 邮箱。', keywords: '证书 ACME Cloudflare DNS Token Let’s Encrypt' },
    { id: 'settings-security', label: '出口安全', note: '内网访问防护', description: '控制节点是否阻断内网、链路本地地址和云元数据。', keywords: '安全 内网 元数据 127 192 169 阻断' },
    { id: 'settings-template', label: '订阅模板', note: '客户端输出', description: '自定义 Clash / sing-box 订阅模板与 Clash UDP 声明。', keywords: '订阅 模板 Clash YAML sing-box JSON UDP' },
    { id: 'settings-runtime', label: '采集与同步', note: '节点负载', description: '调整探针采集、流量统计和健康检查频率。', keywords: '探针 采集 流量 统计 健康检查 同步 间隔 重建' },
  ] },
  { label: '系统维护', sections: [
    { id: 'settings-update', label: '在线更新', note: '版本与令牌', description: '配置在线更新检查使用的 GitHub API 令牌。', keywords: '更新 GitHub Token API 限额' },
    { id: 'settings-backup', label: '数据备份', note: '一致性快照', description: '导出包含全部业务数据的一致性数据库快照。', keywords: '备份 数据库 SQLite WAL 下载 恢复' },
  ] },
]
const settingsSections = settingsGroups.flatMap(group => group.sections)
const settingsSearch = ref('')
const activeSectionId = ref('settings-basic')
const activeSection = computed(() => settingsSections.find(section => section.id === activeSectionId.value) || settingsSections[0])
const activeSectionGroup = computed(() => settingsGroups.find(group => group.sections.some(section => section.id === activeSectionId.value))?.label || '')
const filteredSettingsSections = computed(() => {
  const query = settingsSearch.value.trim().toLocaleLowerCase()
  if (!query) return settingsSections
  return settingsSections.filter(section => `${section.label} ${section.note} ${section.description} ${section.keywords}`.toLocaleLowerCase().includes(query))
})

async function selectSettingsSection(id: string, clearSearch = false) {
  if (!settingsSections.some(section => section.id === id)) return
  activeSectionId.value = id
  if (clearSearch) settingsSearch.value = ''
  const query = { ...route.query, section: id.replace(/^settings-/, '') }
  await router.replace({ query })
  await nextTick()
  const main = document.querySelector('.settings-main')
  if (main) {
    const top = main.getBoundingClientRect().top + window.scrollY - 78
    window.scrollTo({ top: Math.max(0, top), behavior: 'smooth' })
  }
}

function openFirstSearchResult() {
  const first = filteredSettingsSections.value[0]
  if (first) selectSettingsSection(first.id, true)
}

watch(() => route.query.section, value => {
  const id = `settings-${Array.isArray(value) ? value[0] : value || 'basic'}`
  if (settingsSections.some(section => section.id === id)) activeSectionId.value = id
}, { immediate: true })
const loading = ref(false)
const saving = ref(false)
const testingSmtp = ref(false)
const testingTg = ref(false)
const notifyExpiryDays = ref(3)
const notifyTrafficPct = ref(20)
type TelegramPanelID = 'bot' | 'alerts' | 'commands' | 'ops' | 'templates'
const telegramPanels: { id: TelegramPanelID; label: string }[] = [
  { id: 'bot', label: '基础连接' },
  { id: 'alerts', label: '提醒规则' },
  { id: 'commands', label: '自定义指令' },
  { id: 'ops', label: '运维告警' },
  { id: 'templates', label: '消息模板' },
]
const telegramPanel = ref<TelegramPanelID>('bot')
type TelegramCustomCommand = { id: number; command: string; description: string; response: string }
const telegramCustomCommands = ref<TelegramCustomCommand[]>([])
let telegramCustomCommandID = 0
const telegramReservedCommands = new Set(['start', 'help', 'sub', 'plan', 'plans', 'traffic', 'status', 'unbind'])
function addTelegramCustomCommand() {
  if (telegramCustomCommands.value.length >= 20) return
  telegramCustomCommands.value.push({ id: ++telegramCustomCommandID, command: '', description: '', response: '' })
}
function removeTelegramCustomCommand(index: number) { telegramCustomCommands.value.splice(index, 1) }
function loadTelegramCustomCommands(raw: unknown) {
  try {
    const parsed = JSON.parse(typeof raw === 'string' && raw.trim() ? raw : '[]')
    telegramCustomCommands.value = Array.isArray(parsed) ? parsed.map((item: any) => ({
      id: ++telegramCustomCommandID,
      command: String(item?.command || ''),
      description: String(item?.description || ''),
      response: String(item?.response || ''),
    })).slice(0, 20) : []
  } catch {
    telegramCustomCommands.value = []
  }
}
function serializeTelegramCustomCommands(): string {
  const rows = telegramCustomCommands.value.map(item => ({
    command: item.command.trim().replace(/^\//, '').toLowerCase(),
    description: item.description.trim(),
    response: item.response.trim(),
  })).filter(item => item.command || item.description || item.response)
  const seen = new Set<string>()
  for (const [index, item] of rows.entries()) {
    if (!/^[a-z0-9_]{1,32}$/.test(item.command)) throw new Error(`第 ${index + 1} 条指令名称只能包含小写字母、数字和下划线`)
    if (telegramReservedCommands.has(item.command)) throw new Error(`/${item.command} 是内置指令，不能覆盖`)
    if (seen.has(item.command)) throw new Error(`自定义指令 /${item.command} 重复`)
    if (!item.description) throw new Error(`请填写 /${item.command} 的菜单说明`)
    if (!item.response) throw new Error(`请填写 /${item.command} 的回复内容`)
    seen.add(item.command)
  }
  return JSON.stringify(rows)
}
// 运维告警（节点反复重启）。接收人不限角色，故意用「已绑定 Telegram 的账号」
// 而不是「管理员」来列候选：跑机器的人未必有面板管理员账号。
const restartAlertOn = ref(true)
const restartWindowMin = ref(30)
const restartCount = ref(5)
type OpsCandidate = { user_id: number; username: string; is_admin: boolean; tg_name: string; on: boolean }
const opsCandidates = ref<OpsCandidate[]>([])
const opsEffective = ref(0)
const testingOps = ref(false)
const opsUpdating = ref(new Set<number>())
const opsSaveState = ref<'idle' | 'saving' | 'saved' | 'error'>('idle')
const opsSelectedCount = computed(() => opsCandidates.value.filter(candidate => candidate.on).length)
const opsSaveStatusText = computed(() => {
  if (opsUpdating.value.size || opsSaveState.value === 'saving') return '正在自动保存…'
  if (opsSaveState.value === 'saved') return '已自动保存'
  if (opsSaveState.value === 'error') return '保存失败，请重试'
  return '点击整行选择，自动保存'
})
type TgTplVar = { key: string; desc: string }
type TgTplMeta = { key: string; name: string; body: string; vars?: TgTplVar[] }
const tgTplMeta = ref<TgTplMeta[]>([])
const tgTplKey = ref('sub')
const tgTplInput = ref<any>(null)
const tgTplOptions = computed(() => tgTplMeta.value.map(t => ({ label: t.name, value: t.key })))
const currentTgTpl = computed(() => tgTplMeta.value.find(t => t.key === tgTplKey.value))
const currentTgVars = computed(() => currentTgTpl.value?.vars || [])
const currentTgTplField = computed(() => 'tg_tpl_' + tgTplKey.value)
const currentTgTplBody = computed({
  get: () => form[currentTgTplField.value] ?? '',
  set: (v: string) => { form[currentTgTplField.value] = v },
})
const tgSample: Record<string, string> = {
  site: '轻舟', username: 'alice',
  panel: 'https://panel.example', panel_link: '打开面板',
  url: 'https://panel.example/sub/xxxx',
  url_clash: 'https://panel.example/sub/xxxx?format=clash',
  url_singbox: 'https://panel.example/sub/xxxx?format=singbox',
  url_surge: 'https://panel.example/sub/xxxx?format=surge',
  url_base64: 'https://panel.example/sub/xxxx?format=base64',
  bar: '▓▓▓▓▓▓▓▓░░  82%', used: '82.10 GiB', total: '100.00 GiB',
  remaining: '17.90 GiB', remain_pct: '18', unmetered: '',
  summary: '已用 82.10 GiB / 100.00 GiB，剩余 17.90 GiB',
  items: '月付 100G　生效中 · 30 天\n流量　已用 12.00 GiB / 100.00 GiB，剩余 88.00 GiB\n到期　2026-04-20 23:59',
  name: '月付 100G', status: '生效中', duration: ' · 30 天',
  traffic: '已用 12.00 GiB / 100.00 GiB，剩余 88.00 GiB',
  expiry: '2026-04-20 23:59', plan: '月付 100G', left: '3 天',
  footer: '打开面板续费', help: '/sub　订阅地址\n/plan　我的套餐',
}
const tgTplPreview = computed(() => {
  const src = (currentTgTplBody.value || currentTgTpl.value?.body || '').trim() || (currentTgTpl.value?.body || '')
  return src.replace(/\{\{([a-z0-9_]+)\}\}/g, (_: string, k: string) => tgSample[k] ?? `{{${k}}}`)
})
function loadTgTplDefault() {
  const body = currentTgTpl.value?.body
  if (body != null) currentTgTplBody.value = body
}
function resetTgTpl() { currentTgTplBody.value = '' }

function tgToken(key: string) { return '{{' + key + '}}' }

function insertTgVar(key: string) {
  const token = tgToken(key)
  const el: HTMLTextAreaElement | undefined = tgTplInput.value?.textareaEl
    || tgTplInput.value?.$el?.querySelector?.('textarea')
  const cur = currentTgTplBody.value || ''
  if (!el) {
    currentTgTplBody.value = cur + token
    return
  }
  const start = el.selectionStart ?? cur.length
  const end = el.selectionEnd ?? cur.length
  currentTgTplBody.value = cur.slice(0, start) + token + cur.slice(end)
  requestAnimationFrame(() => {
    el.focus()
    const pos = start + token.length
    el.setSelectionRange(pos, pos)
  })
}
const rebuilding = ref(false)
const form = reactive<Record<string, any>>({})
const emailVerify = ref(true)
const pointsRate = ref(10)
const signupBonus = ref(0)
const defaultTraffic = ref(0)
const defaultExpiry = ref(0)
const freeGroupId = ref<number | null>(null)
// 默认 false，与后端 credsResetEnabled() 的「只有显式 true 才算开」保持一致。
const credsResetEnabled = ref(false)
// 默认 true，与后端「缺省即开启，只有显式 '0' 才关」保持一致。
const blockPrivate = ref(true)
const alertCpu = ref(90)
const alertMem = ref(90)
const alertDisk = ref(85)
const alertStreak = ref(2)
const probeIntervalSeconds = ref(60)
const statsIntervalMinutes = ref(10)
const reconcileIntervalMinutes = ref(60)
const userOnlineWindowMinutes = computed(() => {
  const n = Number(statsIntervalMinutes.value) || 10
  const seconds = Math.max(120, n * 120 + 30)
  return Math.round(seconds / 60 * 10) / 10
})
const refundMode = ref('prorated')
const refundBasis = ref('min')
const refundFee = ref(0)
const testEmail = ref('')
const groupOptions = ref<any[]>([])

// Mirror the backend's normalizeBase: trim, drop trailing slashes, default to
// https:// when no scheme is given.
function normalizeBase(v: string): string {
  v = (v || '').trim().replace(/\/+$/, '')
  if (!v) return ''
  if (!v.includes('://')) v = 'https://' + v
  return v
}
// Base used to build the copyable command: the configured address, else the
// address the admin is currently browsing (so the command is never blank).
const effectiveBase = computed(() => normalizeBase(form.public_base) || window.location.origin)
const installCmd = computed(() => `curl -fsSL ${effectiveBase.value}/install-singbox.sh | bash`)

// 只看 smtp_host：后端 currentMailer/mailerConfigured 也是拿这一个字段判定的。
// 被环境变量顶掉时 GET /settings 已经回填了有效值，所以这里读到的就是实际生效的。
const smtpConfigured = computed(() => !!(form.smtp_host || '').trim())
const telegramConfigured = computed(() => !!(form.telegram_bot_token || '').trim())

function envLocked(key: string): boolean {
  return (form._env_keys || '').split(',').includes(key)
}
const runtimeEnvLocked = computed(() => [
  'monitor_probe_interval_seconds',
  'singbox_stats_interval_minutes',
  'singbox_reconcile_interval_minutes',
].some(envLocked))

type NodeHostCandidate = { value: string; source: string; label: string; note: string; recommended?: boolean }
const detecting = ref(false)
const hostCandidates = ref<NodeHostCandidate[]>([])

// 「节点对外地址」自动获取：后端并发探出口 IP，并把面板访问地址、第一台已启用
// 服务器、本机网卡一起作为候选返回。只有一条时直接填，多条时列出来让管理员挑。
async function detectNodeHost() {
  detecting.value = true
  try {
    const res = await apiGet<{ candidates: NodeHostCandidate[] }>('/api/admin/settings/detect-node-host')
    const list = res?.candidates || []
    hostCandidates.value = list
    if (!list.length) { message.warning('没探测到可用地址，请手动填写'); return }
    // 只有「实测出来的那条」才直接填。剩下的（面板访问地址、本机网卡）都带着
    // 「什么时候不能用」的说明，得让人看过再点。
    if (list.length === 1 && list[0].recommended) pickNodeHost(list[0])
  } catch (e: any) { message.error(e.message || '探测失败') } finally { detecting.value = false }
}

function pickNodeHost(c: NodeHostCandidate) {
  form.node_host_override = c.value
  hostCandidates.value = []
  message.success(`已填入 ${c.value}（${c.label}），记得点保存`)
}

async function copyInstall() {
  try {
    await navigator.clipboard.writeText(installCmd.value)
    message.success('已复制安装命令')
  } catch {
    message.error('复制失败，请手动复制')
  }
}

type SettingsDraft = {
  form: Record<string, any>
  values: {
    emailVerify: boolean
    pointsRate: number
    signupBonus: number
    defaultTraffic: number
    defaultExpiry: number
    freeGroupId: number | null
    credsResetEnabled: boolean
    blockPrivate: boolean
    alertCpu: number
    alertMem: number
    alertDisk: number
    alertStreak: number
    probeIntervalSeconds: number
    statsIntervalMinutes: number
    reconcileIntervalMinutes: number
    refundMode: string
    refundBasis: string
    refundFee: number
    notifyExpiryDays: number
    notifyTrafficPct: number
    restartAlertOn: boolean
    restartWindowMin: number
    restartCount: number
  }
  customCommands: Omit<TelegramCustomCommand, 'id'>[]
}

let savedDraft: SettingsDraft | null = null
const savedFlatState = ref<Record<string, string>>({})

function cloneDraft<T>(value: T): T {
  return JSON.parse(JSON.stringify(value))
}

function captureDraft(): SettingsDraft {
  return {
    form: { ...form },
    values: {
      emailVerify: emailVerify.value,
      pointsRate: pointsRate.value,
      signupBonus: signupBonus.value,
      defaultTraffic: defaultTraffic.value,
      defaultExpiry: defaultExpiry.value,
      freeGroupId: freeGroupId.value,
      credsResetEnabled: credsResetEnabled.value,
      blockPrivate: blockPrivate.value,
      alertCpu: alertCpu.value,
      alertMem: alertMem.value,
      alertDisk: alertDisk.value,
      alertStreak: alertStreak.value,
      probeIntervalSeconds: probeIntervalSeconds.value,
      statsIntervalMinutes: statsIntervalMinutes.value,
      reconcileIntervalMinutes: reconcileIntervalMinutes.value,
      refundMode: refundMode.value,
      refundBasis: refundBasis.value,
      refundFee: refundFee.value,
      notifyExpiryDays: notifyExpiryDays.value,
      notifyTrafficPct: notifyTrafficPct.value,
      restartAlertOn: restartAlertOn.value,
      restartWindowMin: restartWindowMin.value,
      restartCount: restartCount.value,
    },
    customCommands: telegramCustomCommands.value.map(({ command, description, response }) => ({ command, description, response })),
  }
}

function flattenDraft(draft: SettingsDraft): Record<string, string> {
  const flat: Record<string, string> = {}
  for (const key of Object.keys(draft.form).sort()) flat[`form.${key}`] = JSON.stringify(draft.form[key] ?? null)
  for (const [key, value] of Object.entries(draft.values)) flat[`value.${key}`] = JSON.stringify(value)
  flat.customCommands = JSON.stringify(draft.customCommands)
  return flat
}

const currentFlatState = computed(() => flattenDraft(captureDraft()))
const dirtyCount = computed(() => {
  if (!settingsLoaded.value || !savedDraft) return 0
  const keys = new Set([...Object.keys(savedFlatState.value), ...Object.keys(currentFlatState.value)])
  let count = 0
  for (const key of keys) if (savedFlatState.value[key] !== currentFlatState.value[key]) count++
  return count
})
const hasUnsavedChanges = computed(() => dirtyCount.value > 0)

function rememberSavedDraft() {
  savedDraft = cloneDraft(captureDraft())
  savedFlatState.value = flattenDraft(savedDraft)
}

function discardChanges() {
  if (!savedDraft) return
  const draft = cloneDraft(savedDraft)
  for (const key of Object.keys(form)) delete form[key]
  Object.assign(form, draft.form)
  emailVerify.value = draft.values.emailVerify
  pointsRate.value = draft.values.pointsRate
  signupBonus.value = draft.values.signupBonus
  defaultTraffic.value = draft.values.defaultTraffic
  defaultExpiry.value = draft.values.defaultExpiry
  freeGroupId.value = draft.values.freeGroupId
  credsResetEnabled.value = draft.values.credsResetEnabled
  blockPrivate.value = draft.values.blockPrivate
  alertCpu.value = draft.values.alertCpu
  alertMem.value = draft.values.alertMem
  alertDisk.value = draft.values.alertDisk
  alertStreak.value = draft.values.alertStreak
  probeIntervalSeconds.value = draft.values.probeIntervalSeconds
  statsIntervalMinutes.value = draft.values.statsIntervalMinutes
  reconcileIntervalMinutes.value = draft.values.reconcileIntervalMinutes
  refundMode.value = draft.values.refundMode
  refundBasis.value = draft.values.refundBasis
  refundFee.value = draft.values.refundFee
  notifyExpiryDays.value = draft.values.notifyExpiryDays
  notifyTrafficPct.value = draft.values.notifyTrafficPct
  restartAlertOn.value = draft.values.restartAlertOn
  restartWindowMin.value = draft.values.restartWindowMin
  restartCount.value = draft.values.restartCount
  telegramCustomCommands.value = draft.customCommands.map(item => ({ id: ++telegramCustomCommandID, ...item }))
  message.success('已恢复到上次保存的设置')
}

function confirmDiscardChanges() {
  dialog.warning({
    title: '放弃未保存的更改？',
    content: `将恢复 ${dirtyCount.value} 项设置，已自动保存的 Telegram 接收人不会受影响。`,
    positiveText: '放弃更改',
    negativeText: '继续编辑',
    onPositiveClick: discardChanges,
  })
}

function handleBeforeUnload(event: BeforeUnloadEvent) {
  if (!hasUnsavedChanges.value) return
  event.preventDefault()
  event.returnValue = ''
}

onBeforeRouteLeave(() => {
  if (!hasUnsavedChanges.value) return true
  return window.confirm('当前有未保存的系统设置，确定要离开吗？')
})

async function handleSave() {
  if (!settingsLoaded.value) {
    message.error('系统配置尚未成功读取，已阻止保存以保护原配置')
    return
  }
  if (reconcileIntervalMinutes.value < statsIntervalMinutes.value) {
    await selectSettingsSection('settings-runtime')
    message.error('完整健康检查间隔不能小于流量统计间隔')
    return
  }
  if (form.help_docs_mode === 'external') {
    try {
      const helpURL = new URL(String(form.help_docs_url || '').trim())
      if (!['http:', 'https:'].includes(helpURL.protocol)) throw new Error()
    } catch {
      await selectSettingsSection('settings-help')
      message.error('外部文档 URL 必须是完整的 http:// 或 https:// 地址')
      return
    }
  }
  let customCommandsJSON = '[]'
  try {
    customCommandsJSON = serializeTelegramCustomCommands()
  } catch (e: any) {
    telegramPanel.value = 'commands'
    await selectSettingsSection('settings-telegram')
    message.error(e.message || '自定义 Telegram 指令有误')
    return
  }
  saving.value = true
  try {
    const body: Record<string, any> = {
      ...form,
      email_verify_required: emailVerify.value ? 'true' : 'false',
      points_per_cny: String(pointsRate.value),
      signup_bonus_points: String(signupBonus.value),
      default_traffic: String(Math.round(defaultTraffic.value * 1024 * 1024 * 1024)),
      default_expiry_days: String(defaultExpiry.value),
      free_group_id: freeGroupId.value ? String(freeGroupId.value) : '',
      node_creds_reset_enabled: credsResetEnabled.value ? 'true' : 'false',
      sb_block_private: blockPrivate.value ? '1' : '0',
      alert_cpu_threshold: String(alertCpu.value),
      alert_mem_threshold: String(alertMem.value),
      alert_disk_threshold: String(alertDisk.value),
      alert_consecutive: String(alertStreak.value),
      monitor_probe_interval_seconds: String(probeIntervalSeconds.value),
      singbox_stats_interval_minutes: String(statsIntervalMinutes.value),
      singbox_reconcile_interval_minutes: String(reconcileIntervalMinutes.value),
      refund_mode: refundMode.value,
      refund_basis: refundBasis.value,
      refund_fee_percent: String(refundFee.value),
      notify_expiry_days: String(notifyExpiryDays.value),
      notify_traffic_percent: String(notifyTrafficPct.value),
      alert_restart_enabled: restartAlertOn.value ? 'true' : 'false',
      alert_restart_window_min: String(restartWindowMin.value),
      alert_restart_count: String(restartCount.value),
      telegram_custom_commands: customCommandsJSON,
    }
    await apiPut('/api/admin/settings', body)
    await config.fetchConfig()
    await reloadOpsRecipients()
    rememberSavedDraft()
    message.success('保存成功')
  } catch (e: any) { message.error(e.message) } finally { saving.value = false }
}

// 载入内置默认模板到输入框，方便对照/微调。保存时若与内置完全一致，后端会存为
// 空（保留「留空用内置」语义），因此载入后原样保存也不会锁死在旧版本。
let defaultTemplates: { clash?: string; singbox?: string } | null = null
async function loadDefaultTemplate(which: 'clash' | 'singbox') {
  try {
    if (!defaultTemplates) defaultTemplates = await apiGet('/api/admin/settings/default-templates')
    if (Array.isArray((defaultTemplates as any)?.telegram) && !tgTplMeta.value.length) {
      tgTplMeta.value = (defaultTemplates as any).telegram
    }
    if (which === 'clash') form.sub_clash_template = defaultTemplates?.clash || ''
    else form.sub_singbox_template = defaultTemplates?.singbox || ''
    message.success('已载入内置默认，可编辑后保存')
  } catch (e: any) { message.error(e.message || '载入失败') }
}

async function handleTestSMTP() {
  if (!testEmail.value) { message.warning('请输入测试收件人'); return }
  testingSmtp.value = true
  try { await apiPost('/api/admin/settings/test-smtp', { to: testEmail.value }); message.success('测试邮件已发送') } catch (e: any) { message.error(e.message) } finally { testingSmtp.value = false }
}

// 接收人列表与「实际能收到的聊天数」都由后端算：解绑、封禁、chat id 写错都会
// 让名单悄悄变空，而这个数字是唯一能看出来的地方。
function applyOpsRecipients(d: any) {
  if (!d) return
  opsCandidates.value = d.candidates || []
  opsEffective.value = d.effective || 0
  if (form.alert_ops_extra_chats === undefined) form.alert_ops_extra_chats = d.extra_chats || ''
}

async function reloadOpsRecipients() {
  applyOpsRecipients(await apiGet<any>('/api/admin/ops-recipients').catch(() => null))
}

async function toggleOpsRecipient(c: OpsCandidate, on: boolean) {
  if (opsUpdating.value.has(c.user_id)) return
  const previous = c.on
  c.on = on
  opsUpdating.value.add(c.user_id)
  opsSaveState.value = 'saving'
  try {
    await apiPut(`/api/admin/ops-recipients/${c.user_id}`, { on })
    await reloadOpsRecipients()
    opsSaveState.value = 'saved'
  } catch (e: any) {
    c.on = previous
    opsSaveState.value = 'error'
    message.error(e.message)
  } finally {
    opsUpdating.value.delete(c.user_id)
  }
}

async function handleTestOpsAlert() {
  testingOps.value = true
  try {
    const d = await apiPost<{ sent?: number; failed?: string[] }>('/api/admin/ops-recipients/test', {})
    if (d?.failed?.length) message.warning(`已发送 ${d.sent || 0} 个，失败 ${d.failed.length} 个：${d.failed[0]}`)
    else message.success(`测试告警已发送给 ${d?.sent || 0} 个聊天`)
  } catch (e: any) { message.error(e.message) } finally { testingOps.value = false }
}

async function handleTestTelegram() {
  testingTg.value = true
  try {
    const data = await apiPost<{ username?: string; sent?: boolean }>('/api/admin/settings/test-telegram', {
      token: form.telegram_bot_token,
    })
    if (data?.username) form.telegram_bot_username = data.username
    message.success(data?.sent ? `Bot @${data.username || ''} 正常，测试消息已发送` : `Bot @${data?.username || ''} 连接正常`)
  } catch (e: any) { message.error(e.message) } finally { testingTg.value = false }
}

const backingUp = ref(false)
async function handleBackup() {
  backingUp.value = true
  try {
    await apiDownload('/api/admin/backup', 'qingzhou-backup.db')
    message.success('备份已开始下载')
  } catch (e: any) { message.error(e.message || '备份失败') } finally { backingUp.value = false }
}

async function handleRebuild() {
  rebuilding.value = true
  try { await apiPost('/api/admin/rebuild'); message.success('重建成功') } catch (e: any) { message.error(e.message) } finally { rebuilding.value = false }
}

function sleep(ms: number) {
  return new Promise(resolve => setTimeout(resolve, ms))
}

async function readSettingsWithRetry(): Promise<Record<string, string>> {
  const retryDelays = [0, 400, 1200, 2400]
  let lastError: any = null
  for (const delay of retryDelays) {
    if (delay) await sleep(delay)
    try {
      const data = await apiGet<Record<string, string>>('/api/admin/settings')
      // Seed always creates several settings. A null/empty response therefore
      // means the request did not produce a usable configuration snapshot; do
      // not turn it into an editable empty form.
      if (!data || typeof data !== 'object' || !Object.keys(data).length) {
        throw new Error('服务器返回了空的配置快照')
      }
      return data
    } catch (e: any) {
      lastError = e
      // Authentication/authorization failures are not transient. The shared
      // API wrapper handles the login redirect; retrying would only add noise.
      if (e?.status === 401 || e?.status === 403) break
    }
  }
  throw lastError || new Error('未知读取错误')
}

async function loadSettings() {
  loading.value = true
  settingsLoaded.value = false
  loadError.value = ''
  try {
    // The settings snapshot is critical and gets a short retry window because
    // an online update re-execs the backend. Optional metadata must not be able
    // to turn a successful settings read into a blank page.
    const data = await readSettingsWithRetry()
    const [groups, defaults, ops] = await Promise.all([
      apiList<any>('/api/admin/node-groups').catch(() => []),
      apiGet<any>('/api/admin/settings/default-templates').catch(() => null),
      apiGet<any>('/api/admin/ops-recipients').catch(() => null),
    ])
    applyOpsRecipients(ops)
    if (Array.isArray(defaults?.telegram)) tgTplMeta.value = defaults.telegram
    defaultTemplates = defaults
    if (data) {
      Object.assign(form, data)
      // 从未设置过的键不会出现在响应里，而 n-input 需要一个受控的空串而不是
      // undefined —— 否则第一次输入前它不是一个受控输入。
      form.node_host_override ??= ''
      form.help_docs_mode = data.help_docs_mode === 'external' ? 'external' : 'builtin'
      form.help_docs_url ??= ''
      emailVerify.value = data.email_verify_required === 'true'
      pointsRate.value = parseInt(data.points_per_cny) || 10
      signupBonus.value = parseInt(data.signup_bonus_points) || 0
      defaultTraffic.value = (parseInt(data.default_traffic) || 0) / (1024 * 1024 * 1024)
      defaultExpiry.value = parseInt(data.default_expiry_days) || 0
      freeGroupId.value = parseInt(data.free_group_id) || null
      credsResetEnabled.value = data.node_creds_reset_enabled === 'true'
      blockPrivate.value = data.sb_block_private !== '0'
      alertCpu.value = parseInt(data.alert_cpu_threshold) || 90
      alertMem.value = parseInt(data.alert_mem_threshold) || 90
      alertDisk.value = parseInt(data.alert_disk_threshold) || 85
      alertStreak.value = parseInt(data.alert_consecutive) || 2
      probeIntervalSeconds.value = parseInt(data.monitor_probe_interval_seconds) || 60
      statsIntervalMinutes.value = parseInt(data.singbox_stats_interval_minutes) || 10
      reconcileIntervalMinutes.value = parseInt(data.singbox_reconcile_interval_minutes) || 60
      refundMode.value = data.refund_mode === 'full' ? 'full' : 'prorated'
      refundBasis.value = ['traffic', 'time', 'min'].includes(data.refund_basis) ? data.refund_basis : 'min'
      refundFee.value = parseFloat(data.refund_fee_percent) || 0
      notifyExpiryDays.value = parseInt(data.notify_expiry_days) || 3
      notifyTrafficPct.value = parseInt(data.notify_traffic_percent) || 20
      restartAlertOn.value = data.alert_restart_enabled !== 'false'
      restartWindowMin.value = parseInt(data.alert_restart_window_min) || 30
      restartCount.value = parseInt(data.alert_restart_count) || 5
      loadTelegramCustomCommands(data.telegram_custom_commands)
      form.alert_ops_extra_chats ??= ''
      form.telegram_bot_token ??= ''
      form.telegram_bot_username ??= ''
      for (const t of tgTplMeta.value) form['tg_tpl_' + t.key] ??= ''
    }
    groupOptions.value = (groups || []).map((g: any) => ({ label: g.name, value: g.id }))
    rememberSavedDraft()
    settingsLoaded.value = true
  } catch (e: any) {
    loadError.value = e?.message || '无法连接到配置接口'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  window.addEventListener('beforeunload', handleBeforeUnload)
  loadSettings()
})
onBeforeUnmount(() => window.removeEventListener('beforeunload', handleBeforeUnload))
</script>

<style scoped>
.settings-hero { display:flex; align-items:flex-start; justify-content:space-between; gap:24px; margin-bottom:20px; }
.settings-hero .page-sub { margin-bottom:0; }
.settings-search { width:min(380px,42vw); }
.settings-layout { display:grid; grid-template-columns:210px minmax(0, 1fr); align-items:start; gap:22px; }
.settings-nav { position:sticky; top:84px; display:flex; flex-direction:column; gap:3px; padding:8px; border:1px solid var(--border); border-radius:14px; background:color-mix(in srgb, var(--card) 94%, transparent); box-shadow:var(--shadow-sm); backdrop-filter:blur(16px); }
.settings-nav-group { padding:12px 9px 5px; color:var(--text-3); font-size:10.5px; font-weight:700; letter-spacing:.08em; }
.settings-nav-group:first-child { padding-top:5px; }
.settings-nav button { position:relative; display:grid; grid-template-columns:1fr auto; align-items:center; gap:8px; min-height:40px; padding:8px 9px; border:0; border-radius:9px; background:transparent; color:var(--text-2); text-align:left; font:inherit; cursor:pointer; transition:background .18s ease, color .18s ease, transform .18s ease; }
.settings-nav button:hover { color:var(--text); background:var(--bg-soft); transform:translateX(2px); }
.settings-nav button.active { color:var(--accent-strong); background:var(--accent-soft); }
.settings-nav button:focus:not(:focus-visible) { outline:none; }
.settings-nav button:focus-visible { outline:2px solid color-mix(in srgb, var(--accent) 65%, transparent); outline-offset:1px; }
.settings-nav button.active::before { position:absolute; left:0; width:3px; height:18px; border-radius:0 3px 3px 0; background:var(--accent); content:''; }
.settings-nav span { font-size:12.5px; font-weight:650; }
.settings-nav small { color:var(--text-3); font-size:10px; white-space:nowrap; }
.settings-search-empty { padding:20px 10px; color:var(--text-3); font-size:12px; text-align:center; }
.settings-main { min-width:0; max-width:920px; }
.settings-section-head { margin:1px 0 13px; }
.settings-section-group { margin-bottom:3px; color:var(--accent); font-size:10.5px; font-weight:700; letter-spacing:.08em; }
.settings-section-head h3 { margin:0; color:var(--text); font-size:20px; font-weight:680; letter-spacing:-.02em; }
.settings-section-head p { margin:4px 0 0; color:var(--text-2); font-size:12.5px; line-height:1.6; }
.settings-load-error { margin-bottom:16px; }
.settings-retry { margin-left:10px; }
.settings-section { margin-bottom:16px; scroll-margin-top:84px; }
.settings-section :deep(.n-form) { max-width:760px; }
.settings-actions { position:sticky; bottom:14px; z-index:8; display:flex; align-items:center; gap:8px; width:100%; padding:10px 12px; border:1px solid var(--border-strong); border-radius:12px; background:color-mix(in srgb, var(--card) 94%, transparent); box-shadow:0 14px 38px rgba(31,41,55,.15); backdrop-filter:blur(18px); }
.settings-dirty-copy { display:flex; min-width:0; flex:1; flex-direction:column; }
.settings-dirty-copy b { color:var(--text); font-size:12.5px; }
.settings-dirty-copy span { color:var(--text-3); font-size:11px; }
.form-hint { margin-top: 4px; font-size: 12px; color: var(--text-3); line-height: 1.5; }
.form-hint a { color: var(--accent-strong); }
.section-intro { max-width:72ch; margin:0 0 14px; color:var(--text-3); font-size:12px; line-height:1.7; }
.inline-action, .inline-field { display:flex; align-items:center; gap:9px; }
.field-stack { display:flex; width:100%; flex-direction:column; gap:3px; }
.restart-condition { display:grid; grid-template-columns:repeat(2,minmax(0,180px)); gap:10px; width:100%; }
.restart-condition label { display:flex; min-width:0; flex-direction:column; gap:5px; }
.restart-condition label > span { color:var(--text-3); font-size:11.5px; }
.restart-condition :deep(.n-input-number) { width:100%; }
.section-operation { display:flex; align-items:center; justify-content:space-between; gap:16px; max-width:760px; margin-top:8px; padding:12px 14px; border:1px solid var(--border); border-radius:10px; background:var(--bg-soft); }
.section-operation b { font-size:12.5px; }
.section-operation p { margin:2px 0 0; color:var(--text-3); font-size:11.5px; }
.warn-box {
  margin-bottom: 14px; padding: 10px 12px; border-radius: 8px;
  background: #fbf3e3; border: 1px solid var(--border); border-left: 3px solid var(--warn);
  font-size: 12.5px; color: var(--text-2); line-height: 1.7;
}
.warn-box ul { margin: 6px 0 0; padding-left: 20px; }
.page-sub { color: var(--text-2); margin-bottom: 22px; }
.cf-guide { background: var(--bg-soft); border: 1px solid var(--border); border-radius: 10px; padding: 12px 14px; margin-bottom: 14px; }
.cf-guide-t { color:var(--text); font-size:12.5px; font-weight:650; cursor:pointer; }
.cf-guide[open] .cf-guide-t { margin-bottom:8px; }
.cf-guide ol { margin: 0; padding-left: 20px; display: flex; flex-direction: column; gap: 6px; }
.cf-guide li { font-size: 12.5px; color: var(--text-2); line-height: 1.6; }
.cf-guide a { color: var(--accent-strong); }
.host-cands { margin-top: 8px; display: flex; flex-direction: column; gap: 6px; }
.host-cands-t { font-size: 12px; color: var(--text-3); }
.host-cand { display: grid; grid-template-columns: auto 1fr; gap: 2px 10px; width: 100%; max-width: 420px;
  text-align: left; background: var(--bg-soft); border: 1px solid var(--border); border-radius: 8px;
  padding: 8px 10px; cursor: pointer; font: inherit; }
.host-cand:hover { border-color: var(--accent-strong); }
.host-cand-v { font-family: monospace; font-size: 13px; color: var(--text); }
.host-cand-l { font-size: 12px; color: var(--text-2); align-self: center; }
.host-cand-n { grid-column: 1 / -1; font-size: 11.5px; color: var(--text-3); line-height: 1.6; }
.cf-guide-n { margin-top: 10px; font-size: 12px; color: var(--text-3); line-height: 1.55; }
.cf-guide code { background: var(--border); padding: 0 4px; border-radius: 4px; }
.tg-subnav { display:flex; gap:4px; overflow-x:auto; margin:-2px 0 18px; padding:3px; border-radius:9px; background:var(--bg-soft); }
.tg-subnav button { flex:0 0 auto; min-height:34px; padding:6px 11px; border:0; border-radius:7px; background:transparent; color:var(--text-2); font:inherit; font-size:12px; font-weight:620; cursor:pointer; }
.tg-subnav button:hover { color:var(--text); background:color-mix(in srgb, var(--card) 66%, transparent); }
.tg-subnav button.active { color:var(--text); background:var(--card); box-shadow:0 1px 3px rgba(28,48,70,.1); }
.tg-subnav button:focus:not(:focus-visible) { outline:none; }
.tg-subnav button:focus-visible { outline:2px solid color-mix(in srgb, var(--accent) 65%, transparent); outline-offset:-1px; }
.tg-panel { min-height:260px; }
.tg-tpl-h { font-size: 13px; font-weight: 650; margin-bottom: 6px; }
.ops-recipients { width:100%; max-width:720px; }
.ops-recipient-summary { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:8px; color:var(--text-2); font-size:12px; }
.ops-live-state { display:inline-flex; align-items:center; gap:6px; color:var(--text-3); }
.ops-live-state i { width:7px; height:7px; border-radius:50%; background:var(--text-3); }
.ops-live-state.is-saving i { background:var(--warn); animation:ops-pulse 1s ease-in-out infinite; }
.ops-live-state.is-saved { color:var(--success); }
.ops-live-state.is-saved i { background:var(--success); }
.ops-live-state.is-error { color:var(--danger); }
.ops-live-state.is-error i { background:var(--danger); }
@keyframes ops-pulse { 50% { opacity:.35; } }
.ops-selected-count { flex:none; padding:2px 8px; border-radius:999px; background:var(--bg-soft); color:var(--text-2); font-size:11.5px; font-weight:600; }
.ops-recipient-list { display:grid; grid-template-columns:repeat(auto-fit,minmax(240px,1fr)); gap:8px; }
.ops-recipient {
  box-sizing:border-box; width:100%; min-height:58px; margin:0; padding:9px 11px;
  align-items:center; border:1px solid var(--border); border-radius:10px; background:var(--card);
  cursor:pointer; transition:border-color .16s ease, background .16s ease, box-shadow .16s ease;
}
.ops-recipient:hover { border-color:var(--accent-strong); background:var(--bg-soft); box-shadow:var(--shadow-sm); }
.ops-recipient--checked { border-color:color-mix(in srgb, var(--accent-strong) 58%, var(--border)); background:color-mix(in srgb, var(--accent) 8%, var(--card)); }
.ops-recipient :deep(.n-checkbox__label) { flex:1; min-width:0; padding-left:10px; }
.ops-recipient-copy { display:flex; min-width:0; flex-direction:column; gap:4px; line-height:1.3; }
.ops-recipient-name { overflow:hidden; color:var(--text); font-size:13px; font-weight:650; text-overflow:ellipsis; white-space:nowrap; }
.ops-recipient-meta { display:flex; min-width:0; align-items:center; gap:7px; color:var(--text-3); font-size:11.5px; }
.ops-recipient-role { flex:none; padding:1px 6px; border-radius:999px; background:var(--bg-soft); color:var(--text-2); }
.ops-recipient-tg { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.ops-recipient-hint { margin-top:8px; }
.tg-custom-list { display:flex; flex-direction:column; gap:10px; margin-bottom:10px; }
.tg-custom-item { padding:10px; border:1px solid var(--border); border-radius:10px; background:var(--bg-soft); }
.tg-custom-head { display:grid; grid-template-columns:minmax(150px,.7fr) minmax(220px,1.3fr) auto; gap:8px; margin-bottom:8px; }
.tg-preview {
  margin-top: 10px; background: #1b2838; color: #e8eef6; border-radius: 10px;
  padding: 10px 12px; max-width: 420px;
}
.tg-preview-h { font-size: 11px; color: #93a4b8; margin-bottom: 6px; }
.tg-preview-body {
  margin: 0; white-space: pre-wrap; word-break: break-word;
  font-family: ui-sans-serif, system-ui, "Segoe UI", "PingFang SC", sans-serif;
  font-size: 13.5px; line-height: 1.55;
}
.tg-vars { margin: 0 0 10px; }
.tg-vars-h { font-size: 12px; font-weight: 650; margin-bottom: 6px; }
.tg-vars-h span { font-weight: 400; color: var(--text-3); }
.tg-vars-list { display: flex; flex-direction: column; gap: 4px; max-width: 560px; }
.tg-var {
  display: grid; grid-template-columns: minmax(140px, auto) 1fr; gap: 8px 12px;
  align-items: baseline; text-align: left; width: 100%;
  background: var(--bg-soft); border: 1px solid var(--border); border-radius: 8px;
  padding: 6px 10px; cursor: pointer; font: inherit; color: inherit;
}
.tg-var:hover { border-color: var(--accent-strong); }
.tg-var code {
  font-size: 12px; background: var(--border); padding: 1px 5px; border-radius: 4px;
  white-space: nowrap;
}
.tg-var span { font-size: 12.5px; color: var(--text-2); line-height: 1.45; }
@media (max-width: 900px) {
  .settings-layout { grid-template-columns:1fr; }
  .settings-nav { top:64px; z-index:6; flex-direction:row; overflow-x:auto; }
  .settings-nav-group { display:none; }
  .settings-nav button { display:flex; flex:0 0 auto; min-height:36px; white-space:nowrap; }
  .settings-nav button.active::before { display:none; }
  .settings-nav small { display:none; }
  .settings-nav button:hover { transform:none; }
  .settings-search-empty { min-width:160px; }
  .settings-main { max-width:none; }
}
@media (max-width: 640px) {
  .settings-hero { flex-direction:column; gap:12px; }
  .settings-search { width:100%; }
  .tg-custom-head { grid-template-columns:1fr; }
  .tg-subnav { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); overflow:visible; }
  .tg-subnav button { width:100%; padding-right:6px; padding-left:6px; }
  .ops-recipient-list { grid-template-columns:1fr; }
  .inline-action, .inline-field { align-items:flex-start; flex-direction:column; }
  .section-operation { align-items:stretch; flex-direction:column; }
  .settings-actions { bottom:8px; flex-wrap:wrap; }
  .settings-dirty-copy { flex-basis:100%; }
}
@media (max-width: 560px) {
  .settings-section :deep(.n-form-item-blank) { min-width:0; width:100%; }
  .settings-section :deep(.n-input),
  .settings-section :deep(.n-input-number),
  .settings-section :deep(.n-select),
  .settings-section :deep(.n-input-group) { width:100% !important; max-width:100% !important; }
  .tg-subnav { margin-right:-6px; margin-left:-6px; }
  .restart-condition { grid-template-columns:repeat(2,minmax(0,1fr)); }
}
</style>
