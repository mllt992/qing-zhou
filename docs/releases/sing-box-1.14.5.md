# 节点内核 1.14.4 → 1.14.5

本次将发行构建锁定为官方 sing-box v1.14.5（提交 `a6844a780ba173e189a018d0ac62df67cd50b8e9`），保留全部现有构建标签，尤其是 `with_v2ray_api` 与 `with_grpc`。amd64 / arm64 发行资产仍由既有 release 流程生成；合并本改动不会发布 release，也不会自动升级现有节点。

官方 v1.14.5 把 sing-vmess 升到 `4acc92a6f6c6`（`v0.2.9-0.20261124075649-4acc92a6f6c6`）。相对 1.14.4 的 `a0c58542bd9d`，这版包含 mux 服务端并发写、packetaddr 写出字节计数，以及 xudp 帧读取修复，并仍覆盖此前的 Vision 修复。发行构建继续只核对官方 go.mod 已引入该版本，不改写 require。

轻舟 Trojan 分段握手维护补丁（#87，`b85be9da…`）仍然需要。v1.14.5 的 `transport/trojan/service.go` 与 v1.14.2 / v1.14.4 文件 hash 相同（`6ffe5d2408d4b391b2e625de0777debecbd02c1a01c1b079f46c18fcac114edf`），补丁在干净官方树上干净应用，补丁 SHA256 不变。上游对「关闭 TLS 时 trojan 崩溃」的修复改的是 `protocol/trojan`，不替代本补丁。当前完整标识为 `1.14.5+qz-trojan.b85be9da`。已安装的历史审定标记（1.14.2 的 Vision / transport / Trojan，以及 `1.14.4+qz-trojan.b85be9da`）在能力门禁中仍然有效。官方裸版本号（含 1.14.5）仍不视为已审定。

安装脚本的官方应急回退也锁定 v1.14.5，不随上游 latest 漂移。官方包仍不含 `with_v2ray_api`，也不含 Trojan 维护补丁，不能提供本面板的逐用户流量计量/配额执行；脚本会明确警告，应重新安装面板托管版本。上游把 Linux 桌面客户端包从 `sing-box` 改名为 `sfl`，不再安装 `/usr/bin/sing-box`。本仓库的节点安装脚本直接下载面板托管的 CLI 二进制，不依赖该桌面包名。

## 构建工具链

上游 v1.14.5 的 `go.mod` 仍是 `go 1.25.5`，没有 `toolchain` 指令。提交 `463c44e7f903`（Update Go to 1.27.4）只改了上游 CI、Dockerfile 和旧系统补丁脚本，没有提高语言版本。直接依赖抽查到的 `go` 行也不高于 1.25.0。因此节点核心继续用 #109 已审定的 **Go 1.25.14** 构建（高于模块声明的 1.25.5）。面板自身的 `go 1.25.0` 与 CI 的 Go 1.25 / 1.25.14 不变。不升到 1.15 alpha。

## 兼容性检查

已对照 [上游 v1.14.4…v1.14.5](https://github.com/SagerNet/sing-box/compare/v1.14.4...v1.14.5)。与节点侧相关的变化包括：VMess/VLESS mux 并发流、关闭 TLS 时的 vless/trojan 崩溃、Shadowsocks 多用户在 SSM API 更新时的流量归属、packetaddr/mux 写出字节计数，以及创建 box 时校验 route rule 的 outbound 与 DNS server 引用。面板生成配置未引入 1.15 alpha 字段，也无需为本次升级改写 JSON 选项。`domain_strategy` 等已弃用字段仍由生成路径剥离。生成配置（含各协议入站）须通过新核心的 `sing-box check`，避免更严的引用校验把原先能启动的配置判失败。

Release 仍以刚构建的二进制执行 `TestGeneratedServerConfigPassesSingboxCheck`，保留生成配置中的 `experimental.v2ray_api`；缺失流量插件或配置校验失败会阻止上传资产。

来源：[1.14.5 release](https://github.com/SagerNet/sing-box/releases/tag/v1.14.5)、[完整 compare](https://github.com/SagerNet/sing-box/compare/v1.14.4...v1.14.5)。
