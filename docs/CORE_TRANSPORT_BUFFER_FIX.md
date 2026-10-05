# 固定核心的 WebSocket / HTTPUpgrade 缓冲修复

## 范围与来源

构建仍基于官方 sing-box v1.14.2（`af6e64c3b69e6132ebaee0e1a3d24e93903f6709`），保留既有官方 sing-vmess `9b95ab8c9478` Vision 修复，不切换 alpha，不升级其余依赖。

额外选取官方提交 [07512b1093aa38626fca00dbba07bc5d77375861](https://github.com/SagerNet/sing-box/commit/07512b1093aa38626fca00dbba07bc5d77375861) 的三个文件补丁片段：

1. `transport/v2rayhttpupgrade/client.go`：HTTP 101 后已有缓冲按实际可用空间读入；错误路径释放缓冲
2. `transport/v2rayhttpupgrade/server.go`：保留 Hijack 返回的已预读字节，不丢代理协议首字节；错误路径释放缓冲
3. `transport/v2raywebsocket/client.go`：保留 HTTP 101 后首个已缓冲 WS 帧；处理子协议头前克隆共享 headers，避免并发握手修改同一 map

这是上述三文件的原样上游 hunks 回补，包含错误清理和同路径 headers.Clone，并非“只改三行”。该官方提交的其它文件/协议修复没有一起引入。

- 完整构建标记：`1.14.2+qz-vmess.9b95ab8c9478-transport.07512b10`（当前发布核心另含 Trojan 项目维护补丁，标记为 `…-transport.07512b10-trojan.b85be9da`，见 [CORE_TROJAN_HANDSHAKE_FIX.md](CORE_TROJAN_HANDSHAKE_FIX.md)；新标记同时满足本修复能力）
- 选定 patch SHA256：`731a661d2121470feb6e04c64d0c6a2b6185819dac9c11fb50af06f2c87e9a44`
- 共享输入：`scripts/singbox-pins.sh`
- 每个文件回补前后 SHA256：`scripts/transport-buffer/manifest.json`

`apply-transport-buffer-patch.sh`验证基础 commit、patch hash、精确三文件集合及应用前后文件 hash；异常时中止。CI 与 Release 均调用同一 `build-singbox.sh`，`sing-box-provenance.json`（schema 3，另含 Trojan 维护补丁 manifest）包含基础源码、Vision 模块、选定 transport patch、工具链和各架构二进制 hash。版本标记不是对任意同名第三方二进制的来源证明，须核对发布的 provenance/hash。

## 确定性回归与完整核心的区别

原始核心的五个固定负向用例确实执行并失败：HTTPUpgrade 客户端 101＋第一段合并、服务端预读 1/4/11 字节，以及 WS 客户端 101＋第一帧合并。服务端只预读 version 字节 0 时，原实现随后把下一字节 146 当成 version；这与真实回环出现的错误一致。

补丁后的库级回归另外覆盖 1/7 字节分片、无负载/正常关闭、最高 1 MiB 数据、32 个并发连接及共享 WS 子协议 headers，执行 race 检查。它们不启动完整核心、不做真实 TLS 或套餐入账，不能替代全协议真实测试。

```sh
QZ_TRANSPORT_STRESS_ROUNDS=100 bash scripts/test-transport-buffer.sh --check-baseline
bash scripts/build-singbox.sh --output-dir "$PWD/.tmp-metering-core" --arch amd64,arm64
```

脚本要求固定 Go 1.25.14。负向对照必须看到指定测试运行并失败，编译失败、网络失败或 `no tests to run` 不算复现；正向必须实际运行且无 skip。两架构编译不等于 arm64 已执行真实网络测试。

实际验收仍要求同一候选核心的 49 条原协议/传输路径、额外三协议 WS 反代长流/并发/中断恢复、已有 Vision 压力，以及每机器原始用户计数、报表和入口单次扣费断言。首轮端口竞争与后续真实 HTTPUpgrade/计数可见性失败均不能被后来的配置检查覆盖；最终结果必须对应精确 commit 的完整 CI 原始日志。

## 运行能力门禁与部署边界

旧完整标记 `1.14.2+qz-vmess.9b95ab8c9478` 继续只证明既有 Vision 能力，不证明本次 transport 修复。新标记需单独识别 `has_transport_read_buffer_fix`；通用版本排序、更高 semver、磁盘文件或历史成功缓存均不能替代实际受管运行进程证明。

相关 P1 WS/HTTPUpgrade 路径的预检与应用使用独立能力要求。控制器在配置应用前后核验实际运行核心；正在生成/下发时发生配置变化也不能用过期能力快照放行。预检说明、具体节点错误与当前轮次的成功/失败须分别显示，不把“已保存”当“已生效”。

修复受管核心不能修复客户设备里仍有同类缺陷的第三方客户端。公共链接的参数 roundtrip/config-check 也不是第三方客户端握手保证。服务端监听 path 是 pathname；把 `?query` 直接写进监听 path 不等于客户端附带 query，不能从参数保真测试推断该监听写法已修复。

本次只在隔离本地/CI开发验证，不修改生产数据库，不自动升级、重启或部署生产节点；合并不等于发新版本。管理员部署前仍需备份、验证制品来源，并计划核心重启造成的连接中断。
