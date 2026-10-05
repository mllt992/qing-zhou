# 逐用户机器观测：协议、传输与验收矩阵

本次扩展延续v0.2.87的独立默认关闭开关、隔离测试、生产数据库和生产节点不操作边界。机器原始用户计数分别入账，套餐只按本次业务的入口实测量扣一次；未知和历史共享来源仍单列，不能倒推或均摊。

## 原生协议能力

| 协议 | 客户入口 | 受管落地/中间跳 | 内部认证 | TCP业务 | UDP业务 |
| --- | --- | --- | --- | --- | --- |
| mixed | 是 | 否 | 既有username/password | HTTP CONNECT或SOCKS | SOCKS5 UDP ASSOCIATE |
| VLESS | 是 | 是 | UUID，TLS/传输对应flow | 是 | XUDP |
| VMess | 是 | 是 | UUID，AEAD/alterId=0 | 是 | 核心VMess封装 |
| Trojan | 是 | 是 | password | 是 | 核心Trojan封装 |
| TUIC | 是 | 是 | UUID和password | QUIC stream | QUIC原生relay |
| Hysteria | 是 | 是 | auth_str | QUIC stream | QUIC原生relay |
| Hysteria2 | 是 | 是 | password | QUIC stream | QUIC原生relay |
| AnyTLS | 是 | 是 | password | 原生会话 | 核心内置UoT |
| SS2022 AES-128 | 是 | 是 | 服务器PSK＋独立用户PSK | 是 | 原生UDP或适用mux |
| SS2022 AES-256 | 是 | 是 | 服务器PSK＋独立用户PSK | 是 | 原生UDP或适用mux |

“是”表示本实现中的路径能力，不能脱离下文测试记录解释为所有设置均已实测。SS经典方法及2022 ChaCha不属于当前受管范围；这不等于声明核心本身不能实现这些方法。mixed不能作本管线落地，HTTP代理本身也不提供UDP。显式限制网络、第三方出口策略、防火墙等仍可能限制实际业务。

所有后续物理跳独立建立“用户、链路、代次”的内部身份，跨协议不改变客户原始UUID、密码、账号和订阅。原生统计和auth_user都使用核心认证用户上下文。源认证、监听配置、目标出站须同时一致；重复统计名、同认证键的影子用户、重复入/出站tag都会撤回就绪，不能让最后一条配置覆盖后仍显示已切换。QUIC复用、AnyTLS会话及SS服务器主密钥不能替代独立的用户认证/统计名。

## 传输与TLS

- VLESS、VMess、Trojan受管出站保留原生TCP、WebSocket、gRPC、HTTPUpgrade、HTTP及QUIC传输设置；配置检查覆盖的组合与真实流量覆盖的组合分别记录
- WebSocket保留path、headers和early-data配置；HTTP保留host/path/method；gRPC保留service_name，不能默默降为TCP
- VLESS Vision继续按实际TLS/transport/flow配置启用；Vision与外层mux不兼容，不通过移除Vision掩盖失败。运行核心能力仍须独立核验
- TLS配置使用实际解析后的证书和域名，并保留客户端信任、ALPN及合法TLS选项；不为测试设置insecure，不依赖系统信任库或全局SSL_CERT_FILE掩盖丢失信任配置
- QUIC协议与QUIC传输使用标准TLS；通用TLS profile中的TCP-only uTLS指纹提示不适用于QUIC，不会宣称已应用Chrome指纹。证书验证、SNI与ALPN继续保留，REALITY及不兼容TLS engine组合明确拒绝
- Hysteria v1保留字符串/数值带宽与字符串混淆；方向按两个端点的发送/接收对应。Hysteria2强制客户端带宽场景须另行覆盖，普通BBR不随意改为固定带宽
- VLESS/VMess/Trojan/SS的合法mux保留enabled/padding；客户线路Brutal速率不照搬给中转机器。SS的物理TCP监听加mux仍可能承载UDP业务，不能把两层network含义混为一谈

TLS客户端高级字段的存在不代表每种组合都合法。例如显式证书和证书公钥pin不能随意并用，最终必须通过同一固定核心的check和实际流量测试。Reality、ECH、特殊TLS引擎、QUIC传输、高级拥塞/混淆等未列入真实路径时，不宣称已经完成端到端验收。

## 自动化测试分层

1. 协议逻辑矩阵：9种可落地协议的81个有序入口/落地组合，检查双用户分阶段应用、用户凭据不变、每用户outbound、noop稳定；这是控制面逻辑，不能替代真实通信
2. 验收负测：实际源或目标的认证、TLS、传输、SS method/rootPSK及outbound附加detour发生变化时撤回就绪；恢复正确配置才重新确认
3. 真实矩阵共49条路径（98个配置/通信子项），其中真实双机：9种同协议路径、mixed分别进入9种落地，分别执行A-only/B-only TCP/UDP、并发连接并校验各机器原始user计数等于各自报表；套餐等于各自入口一次，重复批次不重扣
4. 真实三机：VMess→Trojan→Hysteria2、SS128→TUIC→SS256、Hysteria→AnyTLS→VLESS；A/B走全程，C原凭据直连中段，C仅中段扣费，A/B不混入C计数
5. 传输路径：TLS、Vision、WS early-data、gRPC、HTTPUpgrade、HTTP、QUIC、适用mux等有独立子测试；测试源中的路径列表是精确清单
6. 额外WS稳定性：三协议经临时TLS反代，双用户长流与并发；主动断连后原非重放POST必须失败且目标只执行一次，新请求ID独立恢复，最后仍按原始计数入账
7. 生命周期与规模：保留已发布的旧代晚到、重试、跨进程、失败/noop、迁移回滚测试；扩展协议/证书变更代次和源认证摘要验证，以及VLESS/AnyTLS/TUIC/Hysteria2的100/1000用户配置诊断

真实候选核心固定为`1.14.2+qz-vmess.9b95ab8c9478-transport.07512b10`，与`scripts/build-singbox.sh`和发布产物同源。测试只用回环HTTP/UDP目标、临时证书及临时数据库。真实流量和1000个配置用户不等于1000个活跃连接或公网性能；QUIC/AnyTLS每用户会话成本、旧代次累积、长期SQLite并发需要单独评估。

## 可复现命令和结果状态

```sh
# 配置、状态机与生命周期。部分core相关测试需要设置固定二进制。
go test ./internal/store ./internal/singbox ./internal/sbctl -count=1

# 真实协议矩阵；没有固定核心不得把skip记成通过。
QZ_SINGBOX_TEST_BIN=/path/to/sing-box-linux-amd64 \
QZ_SINGBOX_REQUIRE_STATS=1 \
go test -race ./internal/store -run '^TestMeteringRelayRealSingboxProtocolMatrix$' -count=1 -v

# 包含原有Vision压力及P0的全部真实回环测试。
QZ_SINGBOX_TEST_BIN=/path/to/sing-box-linux-amd64 \
QZ_SINGBOX_REQUIRE_STATS=1 QZ_VISION_STRESS_BATCHES=100 \
go test -race ./internal/store -run '^(TestMeteringRelayRealSingbox|TestRelayVLESSFlowMatchesListener)' -count=1 -v

QZ_METERING_SCALE=1 go test ./internal/store -run '^TestMeteringRelayUserScale' -count=1 -v
```

开发阶段的云沙箱正常启动核心受`create netlink socket: operation not permitted`阻塞。因此本地配置check成功不能计入真实TCP/UDP通过；最终结果须绑定确定commit及具备正常内核能力的隔离CI日志。测试尚在变更时的早先结果不自动覆盖最终commit，不操作系统安全设置来绕过限制。

实现/测试、合并、发版和生产部署分别执行；合并并不等于自动发布或操作生产。发布前按[总验收清单](TRAFFIC_METERING_VALIDATION.md)收口同commit的Go race/vet/build、前端测试/build、脚本检查、固定核心真实流量和规模日志，并明确列出未测能力。

## Trojan 分段首包（#87）

Trojan 入站的密钥可能分多次到达（HTTPUpgrade 预读缓存、TCP/TLS 分段、WS/gRPC/HTTP2 消息边界）。发布核心带轻舟项目维护补丁（非上游修复），按协议边界累积并有界握手；P1 路径中终结 Trojan 入站的机器需独立能力 `has_trojan_handshake_fix`。细节、补丁摘要和验收范围见 [CORE_TROJAN_HANDSHAKE_FIX.md](CORE_TROJAN_HANDSHAKE_FIX.md)。
