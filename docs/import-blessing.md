# 从 Blessing Skin 导入

`atto-import-blessing` 将旧站的账号身份映射、选定角色、原角色 UUID 和当前穿着的皮肤写入 Atto 的 SQLite 数据库。源端为 MySQL/MariaDB，工具只执行读取，不改旧站。

支持 Blessing Skin 5/6 风格的 `users`、`players`、`textures` 表、可配置表前缀，以及 Yggdrasil API 的 `uuid(name, uuid)` 和 Yggdrasil Connect 的 `uuid(pid, name, uuid)`。也识别旧字段名 `player_name`，但要求已有有效的 `tid_skin`；更早版本或插件自定义表结构请先在备份副本完成原站升级。

## 需要准备什么

1. **数据库快照**：可连接的 MySQL 数据库，或先恢复到临时实例的 `.sql` dump。
2. **皮肤图片**：旧站 `storage/textures` 的完整副本；若配置了 `TEXTURES_DIR`、远程文件系统或对象存储，需将实际图片导出为本地文件，按原 SHA-256 命名。
3. **OIDC 身份映射**：目标 issuer，以及每个迁移用户在目标客户端 ID token 中实际使用的 `sub`。
4. **每个旧账号保留哪个角色**：一个旧 UID 只能选择一个 PID。其他角色必须明确标记 `skip`。

**只有 MySQL dump 无法恢复图片。** 数据库里的 `textures.hash` 是图片索引，PNG 存储在文件系统。不要用邮箱、昵称或旧 UID 猜测 OIDC `sub`；从身份提供方的管理导出或该客户端实际签发的 ID token 获取，特别注意 pairwise subject 可能随客户端或 sector 改变。CSV 的 `email` 仅方便管理员查对，不参与绑定。

迁移期间停用旧站写入，确保数据库和图片来自同一时点。目标 Atto 应在首次开放 OIDC 登录前导入；已有自动注册账号不会被工具覆盖或换 UUID。将凭据、dump、图片和 CSV 放在仓库已忽略的 `migration/` 下，或放在仓库外；不要提交真实数据。

## 编译工具

```sh
go build -o atto-import-blessing ./cmd/atto-import-blessing
mkdir -p migration
chmod 700 migration
```

工具不需要 OIDC 服务在线，也不需要客户端密钥。默认超时为 10 分钟，大量数据可传 `-timeout 30m`。完整数据和已选择的皮肤会在内存中校验后一次性写入，适合中小型皮肤站；需要的内存随记录数和选中图片总大小增长。

## 连接源 MySQL

建议在备份副本上操作，并使用只有 `SELECT` 权限的账号。例如，在恢复副本里用管理员执行下面的 SQL，替换密码和数据库名；生产库应将 Host 范围限制为迁移机器：

```sql
CREATE USER 'atto_importer'@'%' IDENTIFIED BY 'replace-with-a-random-password';
GRANT SELECT ON blessing_restore.* TO 'atto_importer'@'%';
```

DSN 只通过环境变量读取，不放在命令行参数中。以下使用 Bash 隐藏输入，避免进入 shell 历史：

```sh
read -r -s -p 'Blessing MySQL DSN: ' BLESSING_MYSQL_DSN
printf '\n'
export BLESSING_MYSQL_DSN
```

输入格式示例（示例中的值需替换）：

```text
atto_importer:password@tcp(127.0.0.1:3307)/blessing_restore?charset=utf8mb4
```

同机 Unix socket 示例：

```text
atto_importer:password@unix(/run/mysqld/mysqld.sock)/blessing_restore
```

公网远程连接使用校验证书的 TLS（DSN 添加 `&tls=true`），或通过 SSH 隧道连接本机端口。工具拒绝 `multiStatements`、任意本地文件上传、明文密码认证及跳过证书验证/回退明文的 TLS 配置。

如果旧站 `.env` 中设置了 `DB_PREFIX=bs_`，以下所有导出和导入命令都加 `-table-prefix bs_`。工具默认没有表前缀，不自动猜测。

## 导出并填写映射表

```sh
./atto-import-blessing -export-mapping migration/players.csv
```

CSV 创建权限为 `0600`；已存在的文件不会被覆盖。用纯文本 CSV 编辑器编辑，保留 UTF-8、引号和完整字符串，不要将 UUID 或 `sub` 转换成数字。原始名称/邮箱属于旧站用户输入；不要让表格软件将单元格内容当作公式执行。

| 列 | 如何处理 |
| --- | --- |
| `action` | 每一行填写 `import` 或 `skip`；默认 `pending` 不可导入，封禁账号默认为 `skip` |
| `uid`, `pid`, `source_name` | 原站身份标识，不修改；工具用它们检查映射是否仍对应源数据 |
| `email`, `permission` | 原站参考信息，不参与 OIDC 绑定；更改 CSV 中的权限不能绕过源站封禁检查 |
| `profile_uuid` | 已知 UUID 自动填入；缺失时需核实旧游戏身份并填写，支持带或不带连字符 |
| `uuid_source` | UUID 来源提示，仅用于人工查看；工具执行时重新从源数据校验 |
| `oidc_subject` | `import` 行必填：目标 OIDC 客户端 ID token 中的精确 `sub` |
| `name` | 导入后的角色名，默认原名；允许改为 3–16 位字母、数字或下划线，UUID 仍保持旧值 |

一个 UID 有多个 PID 时，只能有一行 `import`；其余行保留在文件中并设为 `skip`。不要删除未选择的行，工具要求所有源角色都有明确处理决定。两个旧 UID 也不能映射到同一个 OIDC subject。没有角色的旧账号只计入汇总，不导入；以后首次 OIDC 登录会正常创建新角色。

封禁账号不导入，Atto 也不会复制旧站的封禁策略。需要在 OIDC 提供方限制这些人的客户端访问权限，避免其通过普通 OIDC 注册流程创建账号。

### UUID 如何保留

- 优先读取 `uuid` 表。Connect 结构使用 `pid`，原版 API 结构使用名称。
- 没有对应记录、且 `options.ygg_uuid_algorithm` 明确为 `v3` 时，按 `OfflinePlayer:<原角色名>` 算法推导；保持原名大小写。
- 其他缺失情况显示 `manual-required`，不会随机生成新 UUID。
- 重复记录、无效 UUID、孤立记录或关联名称不一致显示 `review-*`，要求管理员从旧站 API、游戏玩家数据或已核实的备份中确定实际 UUID 后填写。
- 已确定的表内 UUID 或离线 UUID 不允许在 CSV 里更换。若旧站插件在运行时覆盖了这些值，应先在迁移副本修正相应记录，再重新导出映射。

手动补齐的 UUID 是管理员确认的权威值，工具只能校验格式和唯一性，无法知道玩家在旧服务器实际使用哪个 UUID。务必先核实，否则游戏背包、权限和存档可能与原玩家身份分离。

## 试运行，再提交

假设图片位于 `migration/textures`，目标域名的 issuer 为 `https://id.example.com/realms/minecraft`：

```sh
./atto-import-blessing \
  -mapping migration/players.csv \
  -textures-dir migration/textures \
  -oidc-issuer https://id.example.com/realms/minecraft \
  -data-dir ./data
```

默认 **DRY RUN**：校验账号和角色、OIDC 唯一性、UUID、PNG、目标库冲突；不创建、不写入目标 SQLite 数据库。`-oidc-issuer` 也可通过环境变量 `OIDC_ISSUER` 提供，值必须与服务端完全一致。

确认数量与选择无误后，停止目标 Atto，备份已有数据目录，再运行相同命令加 `-apply`：

```sh
./atto-import-blessing \
  -mapping migration/players.csv \
  -textures-dir migration/textures \
  -oidc-issuer https://id.example.com/realms/minecraft \
  -data-dir ./data \
  -apply
```

导入使用一个 SQLite 事务。有冲突或写入失败时，不提交本批任何账号或图片。新目录在 `-apply` 初始化失败时可能留下空数据库，但不会留下部分用户。

完全相同的映射和数据可以重复执行，显示 `identical existing`，不会重置用户后来生成的启动器密钥或会话。若目标账号的角色名、身份或皮肤已经发生变化，则拒绝覆盖。工具也不会将同名角色、同 UUID 或同 OIDC 身份合并到别人的账号。

仅导入被选中角色当前穿着的皮肤。图片需位于 `<hash>` 或 `<hash>.png`，内容必须匹配原 SHA-256；工具会检查尺寸、重新编码 PNG 并按新内容散列存储。缺失图片、非法图片、高清皮肤、64×32 Alex 皮肤等会报错，整批不提交。不会下载 URL、缩放图片或悄悄丢弃所选皮肤。高清数据需先在副本里按 Atto 的 64×64 / 64×32 限制转换，并同步该副本的图片散列和数据库索引。

`tid_skin=-1` 是历史数据迁移标记，工具会要求先完成 Blessing 的数据升级；`tid_skin=0` 才表示未设置皮肤。若源站通过插件从 Mojang 等第三方动态取图，这些未落地的图片不在导入范围内。

导入完成后：

1. 启动 Atto，使用映射中对应的 OIDC 身份登录。
2. 检查角色名、UUID、皮肤和模型。
3. 生成新的启动器密钥，把启动器及游戏服务端的认证地址改为 Atto 的 `/yggdrasil/`。
4. 用目标 Minecraft 版本验证一次登录和进服，确认原存档仍属于同一个 UUID。

旧密码、游戏令牌、网页登录会话、披风、衣柜、皮肤公共库、积分、管理员权限和 RSA 私钥均不导入。Atto 会使用自身的签名密钥；已有启动器配置应重新添加/刷新认证服务器信息。

## 从 MySQL dump 开始

工具不直接解析 SQL 文本。先让相应数据库引擎恢复 dump，再按上述步骤只读导入，避免自行处理 SQL 转义、批量 INSERT、字符集和版本差异。

优先使用与源站相同版本的 MySQL/MariaDB。以下以 **MySQL 8.4** 的临时 Docker 实例为例；MariaDB dump 应优先使用对应版本的 MariaDB 和 `mariadb` 客户端。

### 1. 备份旧站

停止旧站写入后，备份数据库和皮肤目录。例如，MySQL 的单库 InnoDB 备份：

```sh
umask 077
mysqldump -h OLD_MYSQL_HOST -u BACKUP_USER -p \
  --single-transaction --no-tablespaces --set-gtid-purged=OFF \
  blessing > migration/blessing.sql
cp -a /path/to/blessing/storage/textures migration/textures
```

非 InnoDB 表不能仅靠 `--single-transaction` 保证一致性，因此整个备份期间仍需停写。不要只备份 `players`：UUID 和关联皮肤依赖其他表。若只导出必要表，应包含带实际前缀的 `users`、`players`、`textures`、`uuid`、`options`（后两表若存在）。

### 2. 创建隔离的恢复实例

创建 `migration/mysql.env`，设置一个临时随机密码，文件权限设为 `600`：

```dotenv
MYSQL_ROOT_PASSWORD=replace-with-a-random-password
MYSQL_DATABASE=blessing_restore
```

```sh
chmod 600 migration/mysql.env
docker run --rm -d --name atto-blessing-mysql \
  --env-file migration/mysql.env \
  -p 127.0.0.1:3307:3306 \
  mysql:8.4
```

等日志显示数据库已就绪后再恢复：

```sh
docker logs atto-blessing-mysql
```

不要把来源不明的 SQL dump 导入正在使用的数据库实例。恢复过程本来就会执行 dump 中的建表与写入语句，所以这里使用独立临时实例。

### 3. 恢复 SQL

对于**不含 `CREATE DATABASE` / `USE`** 的单库 dump：

```sh
docker exec -i atto-blessing-mysql sh -c \
  'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -u root blessing_restore' \
  < migration/blessing.sql
```

若 dump 来自 `mysqldump --databases` 或 `--all-databases`，通常含原数据库名的 `CREATE DATABASE` / `USE`。在这个独立实例里执行时不指定默认库，随后使用实际恢复出的旧库名：

```sh
docker exec -i atto-blessing-mysql sh -c \
  'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -u root' \
  < migration/blessing.sql
```

命令行指定 `blessing_restore` 不会覆盖 dump 内部的 `USE old_name`。检查实际数据库名，并在后续 GRANT 和 DSN 中使用相同名字；不要用全局文本替换修改整个 SQL 文件。

### 4. 创建只读账号并运行迁移

打开临时实例的 SQL 客户端：

```sh
docker exec -it atto-blessing-mysql sh -c \
  'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -u root'
```

执行前文的 `CREATE USER` 和 `GRANT SELECT`，随后设置 `BLESSING_MYSQL_DSN` 指向 `127.0.0.1:3307`，按「导出映射表 → 试运行 → `-apply`」完成迁移。

确认迁移和游戏联调无误后，停止临时实例（它以 `--rm` 启动，会自动删除临时容器及其匿名卷；不会删除备份文件）：

```sh
docker stop atto-blessing-mysql
unset BLESSING_MYSQL_DSN
```

## 容器部署的目标目录

发行镜像内同时包含 `/usr/local/bin/atto-import-blessing`。目标 Atto 若使用仓库自带 Compose 命名卷，应让迁移工具挂载同一个卷，不能把宿主机的 `./data` 误当成命名卷。

例如，在具备 MySQL 可达地址且备份目录允许容器 UID 10001 读取的环境：

```sh
docker compose stop atto-skin
docker compose run --rm --no-deps \
  -e BLESSING_MYSQL_DSN -e OIDC_ISSUER \
  -v "$PWD/migration:/migration:ro" \
  --entrypoint /usr/local/bin/atto-import-blessing atto-skin \
  -mapping /migration/players.csv \
  -textures-dir /migration/textures \
  -data-dir /data
```

在上面命令末尾添加 `-apply` 才会提交，然后 `docker compose up -d atto-skin`。这里的 MySQL DSN 是**容器内**地址，`127.0.0.1` 指迁移容器自身；将临时数据库与迁移容器接入同一 Docker 网络并使用数据库服务名，或使用你已有的可达地址。不要把备份或 CSV 改为全员可读来处理权限问题，应给执行迁移的账号适当的目录访问权限。

## 测试与结构依据

`go test -race ./...` 包含映射冲突、原子回滚、重复导入、OIDC 身份复用、UUID 保留、PNG 校验与路径限制测试。设置 `ATTO_TEST_MYSQL_DSN` 后，还会用临时前缀表和只读用户运行 MySQL 集成测试；**这个测试 DSN 必须指向可创建用户和表的专用测试数据库，不能是旧站生产库**。CI 使用独立 MySQL 8.4 服务执行这些测试。

结构依据：

- [Blessing Skin Player 模型](https://github.com/bs-community/blessing-skin-server/blob/dev/app/Models/Player.php)、[Texture 模型](https://github.com/bs-community/blessing-skin-server/blob/dev/app/Models/Texture.php)、[纹理存储配置](https://github.com/bs-community/blessing-skin-server/blob/dev/config/filesystems.php)。
- [Yggdrasil API UUID 查询及生成](https://github.com/bs-community/blessing-skin-plugins/blob/master/plugins/yggdrasil-api/src/Models/Profile.php)、[Yggdrasil Connect UUID 模型](https://github.com/bs-community/blessing-skin-plugins/blob/master/plugins/yggdrasil-connect/src/Models/UUID.php)。
- [MySQL SQL 格式备份恢复说明](https://dev.mysql.com/doc/refman/8.4/en/reloading-sql-format-dumps.html)。
