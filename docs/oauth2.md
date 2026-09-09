# 接入认证中心（OAuth2 / OIDC）

入口：轻舟管理后台 → 系统设置 → OAuth2 / OIDC。认证中心需提供标准 OIDC discovery，轻舟自动读取授权、令牌、JWKS、userinfo 地址。

## 认证中心应用

在认证中心创建 Web/OIDC 应用：

- 授权模式：Authorization Code；启用 PKCE S256。
- 回调地址：`https://你的轻舟域名/api/auth/oauth2/callback`，必须完整、精确匹配；没有 `#`、查询参数或末尾斜杠。
- Scope：`openid profile email`。轻舟不请求离线访问或刷新令牌。
- 机密客户端：复制 Client ID 和 Client Secret，令牌认证方式为 `client_secret_basic`。公共客户端可以留空 Secret，使用 `none`。

认证中心项目已提供 `/.well-known/openid-configuration`、`/oauth/authorize`、`/oauth/token`、`/.well-known/jwks.json` 和 `/oauth/userinfo`，按其公开协议实现接入。Issuer 必须与认证中心 APP_ORIGIN / discovery 中 issuer 一致。

## 轻舟配置

填写登录按钮名称、Issuer、Client ID、Client Secret 和上述回调地址。两端均使用有效证书的公开 HTTPS 域名；Issuer 的全部认证端点须同源。私网/回环地址、跨域认证端点和 HTTP 地址不受支持。

点击“测试认证中心连接”检查 discovery 及端点；该测试不验证 Client Secret。保存并启用后，登录弹窗显示“使用认证中心登录”，再完成一次实际授权验证客户端密钥和回调登记。

密钥保存后显示 `***`，不修改即保留；主动清空则切换为公共客户端。配置更改使未完成的旧登录流程失效，请重新发起。

## 账号规则

已有轻舟账号：先用本地账号登录，进入账户设置，输入当前轻舟密码并授权绑定。绑定只增加该账号的登录方式；不能覆盖其他绑定，不按邮箱或同名账号自动合并，也不采用认证中心下发的管理员角色。

新用户：管理员可启用“允许首次登录创建轻舟账号”，默认关闭。还必须满足本站开放注册；邀请码或关闭注册模式应先按本站流程注册再绑定。开启邮箱验证要求时，认证中心须返回已验证邮箱。邮箱已属于现有轻舟账号时，必须先登录该账号绑定。

自动创建的用户名为 `oauth_` 加随机标识，角色为普通用户，适用本站首次积分/额度配置。不会为用户分配一个已知的本地密码；需要本地密码时，可在已配置邮件服务的站点通过已验证邮箱找回密码，或联系管理员。停用账号不能通过认证中心重新登录。

## 故障处理

- “登录请求已过期或失效”：使用同一浏览器重新发起；状态有效期 10 分钟且只能使用一次，同时只保留最近一次发起的流程。
- “认证中心验证失败”：检查公开 HTTPS、Issuer、Client ID/Secret、精确回调地址、S256、系统时钟、签名密钥及 userinfo subject。错误页面不会展示令牌或原始上游错误。
- “尚未绑定”：按本站注册模式先创建本地账号并绑定，或检查自动开户及邮箱验证条件。
- 停用接入：关闭“启用认证中心登录”。现有轻舟会话仍按轻舟会话策略管理，不提供认证中心退出联动。

升级自动新增 `oauth_identities` 与 `oauth_states`，无需手工改表。密钥沿用站点的 `QZ_SECRET_KEY` 加密体系；备份/恢复应保留一致的加密密钥。

实现依据：[go-oidc 文档](https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc)、[OAuth 2.0 安全最佳实践 RFC 9700](https://www.rfc-editor.org/rfc/rfc9700)。
