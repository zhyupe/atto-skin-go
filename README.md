# Atto Skin

用 Go 实现的轻量 Minecraft 皮肤与外置认证服务。功能范围参考 [Blessing Skin](https://github.com/bs-community/blessing-skin-server)，实现独立编写。

- 通过 OIDC 登录网页；每个 `(issuer, subject)` 对应一个角色。
- 修改角色名，保持 UUID 不变；角色名不区分大小写且全站唯一。
- 上传、预览、下载、删除 PNG 皮肤，支持 Steve / Alex 模型。
- Yggdrasil / authlib-injector 登录、令牌管理、进服验证、签名皮肤属性。
- SQLite 持久化，页面和静态资源嵌入 Go 二进制，无前端构建步骤。

不包含披风、皮肤公共库、多角色、站内密码注册或管理员后台。

## 从 Blessing Skin 迁移

提供 `atto-import-blessing` 命令：只读连接旧站 MySQL，导出账号/角色映射表，按显式指定的 OIDC `sub` 导入单角色与当前皮肤，并保留原 UUID。默认试运行，传入 `-apply` 才会提交；遇到冲突整批回滚，重复导入相同内容不会重置账号。

```sh
go build -o atto-import-blessing ./cmd/atto-import-blessing
./atto-import-blessing -help
```

完整步骤、映射表说明和 **MySQL dump 恢复后导入** 的方法见 [Blessing Skin 迁移指南](docs/import-blessing.md)。数据库备份之外，还需要旧站的 `storage/textures` 图片目录。旧密码和游戏令牌不迁移，用户通过 OIDC 登录后生成新的启动器密钥。

## 运行

需要 Go 1.26 或更新版本。使用环境变量配置；程序本身不自动读取 `.env`。

```sh
cp .env.example .env
# 修改 .env 中的 OIDC 信息，然后加载：
set -a
. ./.env
set +a

go build -o atto-skin-go ./cmd/atto-skin-go
./atto-skin-go
```

默认访问 `http://localhost:8080`。服务启动时会连接 OIDC discovery endpoint；提供方不可用或配置无效时，启动会失败。

也可以使用 Docker Compose（自动读取 `.env`）：

```sh
cp .env.example .env
# 填好 OIDC 信息
docker compose up -d --build
```

Compose 默认只将端口暴露在宿主机 `127.0.0.1:8080`，数据写入命名卷 `atto-data`。公网部署需在前面配置 HTTPS 反向代理，并将 `BASE_URL` 改为公网 HTTPS 地址。

## OIDC 配置

在身份提供方创建一个 OpenID Connect Web 客户端，启用 Authorization Code flow 和 PKCE S256。回调地址必须完整匹配：

```text
https://skin.example.com/auth/callback
```

本地开发的回调地址为 `http://localhost:8080/auth/callback`。请求的 scope 为 `openid profile`，ID token 必须包含 `sub` 和请求中的 `nonce`。

| 环境变量 | 默认值 / 含义 |
| --- | --- |
| `BASE_URL` | `http://localhost:8080`；对外访问的源地址，不支持子路径 |
| `LISTEN_ADDR` | `:8080` |
| `DATA_DIR` | `./data`；SQLite 数据文件所在目录 |
| `OIDC_ISSUER` | 必填；提供方 issuer URL，支持 issuer 自带路径 |
| `OIDC_CLIENT_ID` | 必填 |
| `OIDC_CLIENT_SECRET` | 客户端密钥；使用公开客户端时可以为空 |
| `TRUSTED_PROXIES` | 默认不信任任何代理；逗号分隔的代理 CIDR |

`BASE_URL` 和 `OIDC_ISSUER` 仅允许 HTTPS；回环地址允许 HTTP，供本地开发使用。Identity 绑定 issuer 和 subject，邮箱或昵称改变不会新建角色。更换 issuer 会建立新的身份映射。

所有通过该 OIDC 客户端成功认证的用户都可进入站点。若只允许某个组织或用户组，请在身份提供方设置该客户端的访问策略。网页登录有效期为 24 小时，最多保留 10 个网页会话。

## 在 Minecraft 中使用

1. 在网页点击「使用统一账号登录」。首次登录会创建一个默认名为 `p_…` 的角色。
2. 修改角色名，上传皮肤，选择经典或纤细模型。
3. 点击「生成启动器密钥」，保存显示的密钥。
4. 在支持 authlib-injector 的启动器中添加外置登录：
   - 认证服务器地址：`https://skin.example.com/yggdrasil/`
   - 用户名：页面中的角色名
   - 密码：生成的启动器密钥
5. Minecraft 客户端和服务端均使用 authlib-injector，并指向同一个认证服务器；服务端使用 `online-mode=true`。

服务端 Java 参数示例（自行获取 authlib-injector JAR）：

```sh
java -javaagent:authlib-injector.jar=https://skin.example.com/yggdrasil/ -jar server.jar nogui
```

启动器的传统 Yggdrasil 密码接口不能直接执行浏览器 OIDC 跳转，因此使用独立的随机启动器密钥。它不是身份提供方密码，也不是 OIDC access token。密钥完整内容仅在生成时显示，数据库只保存 SHA-256 摘要。密钥为 256 位随机值，不接受用户设置的弱密码。

重新生成密钥会撤销之前的全部游戏令牌和待验证进服记录。游戏令牌有效期为 15 天，每个用户最多保留 10 个；刷新操作原子地撤销旧令牌并发出新令牌。网页退出只结束当前网页会话；启动器的 `signout` 撤销该用户的全部游戏令牌。

改名不会改变 UUID，但会将已有游戏令牌标记为需要刷新，启动器刷新登录后会取得新名字。UUID 为随机 UUID v4，不自动继承已有正版或离线服务器的 UUID；迁移既有服务器时需要自行迁移玩家数据。

## 皮肤与存储

只接受最大 1 MiB 的 PNG，尺寸为 64×64 或 64×32；纤细模型必须为 64×64。先检查图片尺寸，再解码并重新编码，移除原始 PNG 中的附加数据。暂不支持高清皮肤。

皮肤以重新编码后的 SHA-256 为 URL 文件名，按内容去重。所有角色与皮肤查询均为公开接口，供游戏客户端下载。删除皮肤指解除角色当前皮肤关联；已发布的历史纹理仍可通过原有 URL 下载，避免破坏客户端缓存。当前不自动清理历史纹理。

`DATA_DIR/atto.db` 保存账号、皮肤、会话和持久化 RSA 签名私钥。运行时还会有 SQLite WAL 文件。备份时停止进程后备份整个数据目录，或使用 SQLite 的在线备份机制；不要仅复制正在写入的 `.db` 文件。恢复时需保留签名私钥所在数据库。数据库目录应只允许服务账号访问。

部署为单实例。当前进程中的请求限流器不是分布式限流器，SQLite 也不用于多实例共享网络文件系统。

## 反向代理

代理必须保留请求路径和上传内容。设置 `BASE_URL` 为外部 HTTPS 源地址即可正确生成安全 Cookie、OIDC 回调和皮肤 URL。允许至少约 1.1 MiB 的请求体。

默认忽略所有转发 IP 头。只有启用游戏服务端的 `prevent-proxy-connections`，或希望按实际客户端 IP 限流时，才需要配置可信代理：

```text
TRUSTED_PROXIES=127.0.0.1/32,::1/128
```

代理必须**覆盖** `X-Real-IP` 为实际客户端 IP，不能透传用户提供的值。只信任实际代理的 CIDR；容器部署应填代理的实际容器地址或受控网段。程序不使用 `X-Forwarded-For`。

## 协议接口

API Root 为 `/yggdrasil/`；站点响应提供 `X-Authlib-Injector-API-Location`，支持认证服务器地址发现。

| 方法 | 相对于 API Root 的路径 | 功能 |
| --- | --- | --- |
| GET | `/` | 元数据、皮肤域名白名单、RSA 公钥 |
| POST | `/authserver/authenticate` | 角色名 + 启动器密钥登录 |
| POST | `/authserver/refresh` | 原子轮换令牌 |
| POST | `/authserver/validate` | 验证令牌 |
| POST | `/authserver/invalidate` | 撤销单个令牌，幂等 |
| POST | `/authserver/signout` | 撤销账号全部游戏令牌 |
| POST | `/sessionserver/session/minecraft/join` | 提交 30 秒有效的进服凭证 |
| GET | `/sessionserver/session/minecraft/hasJoined` | 校验角色、serverId 和可选 IP，返回签名属性 |
| GET | `/sessionserver/session/minecraft/profile/{uuid}` | 角色与皮肤属性；`unsigned=false` 时签名 |
| POST | `/api/profiles/minecraft` | 批量按名称查找，最多 100 项 |
| GET | `/api/users/profiles/minecraft/{name}` | 单个名称查询 |
| PUT / DELETE | `/api/user/profile/{uuid}/skin` | 通过 Bearer 游戏令牌上传或移除皮肤 |

协议上传使用 `multipart/form-data`，字段为 `file`、`model`；`model` 为 `slim` 或空字符串，另接受 `default`。每个令牌已绑定唯一角色，不需要调用 `refresh` 选择角色。`refresh` 携带 `selectedProfile` 会返回错误。

按 [authlib-injector 服务端规范](https://yushijinhun.github.io/authlib-injector/en/yggdrasil-server-technical-specification.html) 生成 RSA/SHA-1 属性签名；此算法仅用于协议要求的属性签名。OIDC JWT 验证由 `go-oidc` 完成。

只实现以上 Yggdrasil 接口，不提供 Mojang 账号、正版授权或玩家聊天证书服务。Minecraft 版本及启动器兼容性需使用目标版本进行实机联调。

## 开发与检查

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./cmd/atto-skin-go
```

测试包含本机模拟 OIDC discovery、JWKS、签名 ID token 和 PKCE token exchange，不需要真实身份提供方。覆盖身份稳定性、回调重放、签名/issuer/audience/nonce/过期校验、CSRF、并发刷新、令牌上限、单角色约束、纹理重编码、跨账号上传保护和进服凭证。

健康检查：`GET /healthz`（检查数据库可用性）。

代码目录：`cmd/atto-skin-go` 为进程入口，`internal/server` 为 HTTP 服务、OIDC、存储和协议实现，`internal/server/web` 为内嵌页面。
