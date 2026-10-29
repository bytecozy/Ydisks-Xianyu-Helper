# 货源台专用商品接口（goofish fork）

本文件只存在于 `bytecozy/Ydisks-Xianyu-Helper` 的 `goofish` 分支。分支基于上游 tag `v1.0.14`，
镜像 tag 形如 `ghcr.io/bytecozy/ydisks-xianyu-helper:v1.0.14-goofish.N`。

货源台（佳佳游戏库铺货）通过这组接口在服务器上发布、编辑、下架闲鱼商品和对账，不再依赖运营电脑上的 Chrome 插件。

## 鉴权

与其他 `/api/v1` 接口相同：先 `POST /api/v1/session/login` 拿会话 Cookie，之后每个请求带上它。
`cookie_id` 必须属于当前登录用户，否则 403；未登录 401。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/goofish/items/{cookie_id}/edit-detail?item_id=` | 原样返回 `mtop.idle.pc.idleitem.editDetail` 1.0 的 `data`（只读，排查用） |
| POST | `/api/v1/goofish/items/publish` | 按模板商品发布（multipart） |
| POST | `/api/v1/goofish/items/{cookie_id}/{item_id}/edit` | 改标题/描述/价格/图片（multipart） |
| POST | `/api/v1/goofish/items/{cookie_id}/{item_id}/downshelf` | 下架 |
| GET | `/api/v1/goofish/items/{cookie_id}/on-sale?page=N` | 一页在售商品（20 条），不写本地库 |

### POST /api/v1/goofish/items/publish

multipart 字段：

- `cookie_id`：闲鱼账号。
- `template_item_id`：同账号下的模板商品（建议专门留一个下架的模板商品）。
- `title`：标题，不能换行。
- `description`：标题之后的正文（可含 emoji、换行）。
- `price`：售价，单位元，最多两位小数，必须大于 0。
- `images`：重复的文件字段，按顺序 1–9 张，单张 ≤10 MiB。

成功：`200 {"success":true,"item_id":"…","item_url":"https://www.goofish.com/item?id=…"}`，
本地同步失败时额外带 `"warnings":[…]`（闲鱼那边已经发布成功）。

### POST /api/v1/goofish/items/{cookie_id}/{item_id}/edit

multipart 字段都可选，至少给一个：`title`、`description`、`price`、`images`（给了就整组按顺序替换）。
成功：`200 {"success":true,"item_id":"…"}`（可能带 `warnings`）。

注意：PC 编辑页对**已下架**商品提交 edit 会重新上架（DoLovya/pyxianyu 记录的「重新上架」链路）。
不要用 edit 修改已下架、且不想上架的商品。

### POST /api/v1/goofish/items/{cookie_id}/{item_id}/downshelf

成功：`200 {"success":true,"item_id":"…","status":"off_shelf"}`。
商品本来就不在售：`409 already_off_shelf`；商品不存在：`404 item_not_found`。

### GET /api/v1/goofish/items/{cookie_id}/on-sale?page=N

`200 {"success":true,"items":[{"item_id","title","price","status"}],"has_more":bool,"page":N}`。
`status` 为 `on_sale`（列表卡片 `itemStatus=0`），其他值原样带出为 `item_status_<n>`。

## 错误约定

返回体统一 `{"code","message"}`（可能附带 `request_id`）。货源台按下表判断「肯定没做成」还是「不确定」：

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `invalid_request` / `bad_request` | 参数错误（`bad_request` 来自上游 multipart 解析）、模板商品不存在/不可用（多规格、缺类目）。**没改到闲鱼** |
| 401 | `unauthorized` | 未登录。**没改到闲鱼** |
| 403 | `forbidden` | 账号不属于当前用户。**没改到闲鱼** |
| 404 | `item_not_found` | 闲鱼说商品不存在/不属于该账号。**没改到闲鱼** |
| 409 | `account_offline` | 账号凭证不可用或登录 Session 失效（同时在后台触发账号恢复）。**没改到闲鱼** |
| 409 | `risk_control` | 触发风控（RGV587、FAIL_SYS_USER_VALIDATE、punish、captcha、x5secdata），不重试。**没改到闲鱼** |
| 409 | `already_off_shelf` | 下架时商品本来就不在售。**没改到闲鱼** |
| 502 | `publish_failed` / `edit_failed` / `downshelf_failed` / `on_sale_failed` / `edit_detail_failed` | 闲鱼明确拒绝（message 带闲鱼原文），或者写请求根本没发出（前置的 editDetail / 图片上传失败、连接没建立、令牌重签失败）。**没改到闲鱼** |
| 502 | `publish_uncertain` / `edit_uncertain` / `downshelf_uncertain` | 写请求已发出但结果不明：超时、连接中断、HTTP 非 2xx、响应不是 JSON、`FAIL_SYS_*` 系统错误、ret 为空、发布成功却没有 item_id。**需要人工确认** |
| 500 | `internal_error` | 只会出现在调用闲鱼之前（查账号失败）。**没改到闲鱼** |
| 503 | `goofish_items_unavailable` | 接口没装配（不应出现）。**没改到闲鱼** |

分类细节（`internal/xianyu/mtop/goofish_call.go`）：

- 每次请求用 `httptrace.WroteRequest` 判断请求是否已写出；没写出的传输错误算 `_failed`，写出后的算 `_uncertain`。
- `FAIL_SYS_TOKEN_*`：网关在业务前拒绝，刷新 `_m_h5_tk` 后重试（最多 3 次），不会重复执行。
- `FAIL_SYS_API_NOT_FOUNDED`、`FAIL_SYS_ILLEGAL_ACCESS`、`FAIL_SYS_FLOWLIMIT`、`FAIL_SYS_PARAMINVALID`、`FAIL_SYS_ILLEGAL_ARGUMENT`：网关拒绝，算 `_failed`。
- 其他 `FAIL_SYS_*` 和空 ret：写请求算 `_uncertain`，读请求算 `_failed`。
- 其余非 SUCCESS ret（`FAIL_BIZ_*` 或业务自定义码）：`_failed`，message 带原文。
- 风控：ret 命中风控关键字，或响应不是 JSON 且正文/跳转地址含 punish/captcha/x5sec → `409 risk_control`，不重试。
- 「不存在」按 ret 关键字（`NOT_EXIST`、`NOT_FOUND`、`不存在`、`已删除`、`无权`…）识别；「本来就已下架」按（`已下架`、`下架状态`、`不在售`、`OFF_SHELF`、`ITEM_STATUS`…）识别。未命中的业务拒绝仍是 `downshelf_failed`，原文在 message 里。

## 发给闲鱼的 data

所有请求都走 `https://h5api.m.goofish.com/h5/<api>/<v>/`，query 与上游发布相同（`jsv=2.7.2`、`appKey`、`t`、`sign`、`type=originaljson`、`accountSite=xianyu`、`sessionOption=AutoLoginOnly`、`spm_cnt`、`spm_pre`、`log_id`），body 为 `data=<JSON>`。

### editDetail（`mtop.idle.pc.idleitem.editDetail` 1.0）

```json
{"itemId": "<item_id>"}
```

### publish（`mtop.idle.pc.idleitem.publish` 1.0）

先 editDetail 读模板，再把图片逐张传到 `stream-upload.goofish.com/api/upload.api`（上游现有上传），最后：

| 字段 | 来源 |
| --- | --- |
| `itemCatDTO` | 模板 `itemCatDTO` 的 `catId`、`catName`、`channelCatId`、`tbCatId` |
| `itemLabelExtList` | 模板原样（分类=成品号、游戏名称、客户端=全平台互通等类目属性） |
| `itemPostFeeDTO` | 模板原样，`canFreeShipping`/`supportFreight`/`onlyTakeSelf` 转成布尔（无需邮寄） |
| `itemAddrDTO` | 模板原样（发货地），模板没有则不带 |
| `quantity`、`simpleItem`、`itemTypeStr`、`freebies` | 模板值，缺省 `"1"`、`"true"`、`"b"`、`false` |
| `userRightsProtocols` | 模板原样，`enable` 转布尔；模板没有则 `[{"serviceCode":"SKILL_PLAY_NO_MIND","enable":false}]` |
| `itemTextDTO` | 复制模板的 itemTextDTO，`title` = 请求 title，`desc` = `title + "\n\n" + description`（正文第一行已是标题则原样），`titleDescSeparate` 转布尔，缺省 `false` |
| `itemPriceDTO` | `{"priceInCent": "<分>"}`（不带原价） |
| `imageInfoDOList` | 新上传图片，第一张 `major:true`，格式同上游 `publishImagePayload` |
| `defaultPrice` | `false` |
| `uniqueCode` | 当前微秒时间戳 |
| `sourceId`、`bizcode`、`publishScene` | `"pcMainPublish"` |

不带模板的 `itemId`、`itemStatus`、`itemSkuList`、`asyncSecurityInfo` 等字段。模板是多规格商品时直接 400。

成功后与上游 `publishItem` 一样 upsert 本地 `item_info`（标题、整段描述、类目、价格、详情 JSON 里带 `item_image`、`web_url`、`goofish_template_item_id`、`goofish_status=on_sale`），Ydisks 自动回复靠它。

### edit（`mtop.idle.pc.idleitem.edit` 1.0）

先 editDetail，再按白名单把字段带回去（字段表参考 DoLovya/pyxianyu 对 PC 编辑页的整理，MIT）：

- 标量：`attribute_biz_line`、`bizcode`、`bucketId`、`canBargain`、`defaultPrice`、`freebies`、`itemTypeStr`、`quantity`、`scene`、`simpleItem`、`supportBargainPrice`、`topics`
- 对象：`asyncSecurityInfo`、`baseParams`、`itemAddrDTO`、`itemCatDTO`、`itemGroupDTO`、`itemPostFeeDTO`、`itemPriceDTO`、`itemTextDTO`、`itemTopicParams`、`yhbItemInfoDTO`
- 列表：`imageInfoDOList`、`itemLabelExtList`、`itemProperties`、`itemSkuList`、`userRightsProtocols`

然后：

- `itemId` = 路径里的 item_id；`uniqueCode` 新生成；`sourceId`/`bizcode`/`publishScene` 用 editDetail 里的值，没有则 `"pcMainPublish"`。
- `itemPostFeeDTO`、`userRightsProtocols` 布尔字段标准化。
- 给了 `title`/`description`：拆出现有标题和正文（描述第一行等于标题时去掉第一行和空行），替换对应部分后重新拼 `desc`。
- 给了 `price`：`itemPriceDTO.priceInCent` 改为新值；原价 `origPriceInCent` 不高于新售价时去掉。多规格商品改价直接 400。
- 给了 `images`：上传后整组替换 `imageInfoDOList`。

成功后同步本地 `item_info`（保留多规格/多数量开关，详情 JSON 合并已有字段）。

### downshelf（`mtop.taobao.idle.item.downshelf` 2.0）

```json
{"itemId": "<item_id>"}
```

ret SUCCESS 但 `data.success=false` 算 `downshelf_failed`。成功或 `already_off_shelf` 时，本地 `item_info` 存在则在详情 JSON 里记 `goofish_status=off_shelf`、`goofish_off_shelf_at`（不删除记录）。

### on-sale（`mtop.idle.web.xyh.item.list` 1.0）

与上游商品同步相同的在售分组参数：

```json
{"needGroupInfo":false,"pageNumber":N,"pageSize":20,"groupName":"在售","groupId":"58877261","defaultGroup":true,"userId":"<Cookie 里的 unb>"}
```

`has_more` 取 `data.nextPage`，没有该字段时用 `totalCount` 推算。

## 节流与凭证

- 同一账号的每次闲鱼请求（editDetail、每张图片上传、publish/edit/downshelf、列表）之间至少间隔 2 秒，按预约时间槽排队（`internal/adapter/goofish_items.go` 的 `goofishPacer`，进程内）。发布 9 张图约需 20 秒以上。
- 整条链路 100 秒超时，保证在 HTTP WriteTimeout（2 分钟）前给出明确结果。
- 凭证读取、Cookie 会话（含完整 Cookie Jar）写回、运行实例 Cookie 同步复用上游 `ItemPublishPort`；期间凭证被别的流程改过则不覆盖。
- 登录失效时在后台触发上游的账号恢复，本次请求直接返回 `409 account_offline`，不自动重试。

## 与上游 v1.0.14 的差异

新增文件（尽量独立，方便 rebase）：

- `internal/xianyu/mtop/goofish_items.go`、`goofish_call.go`、`goofish_payload.go`（+测试）：对外方法、MTOP 调用与错误分类、载荷构造。
- `internal/application/goofishitems/goofishitems.go`：应用层模型和端口。
- `internal/adapter/goofish_items.go`（+测试）：凭证、节流、item_info 同步。
- `internal/server/goofish_item_handlers.go`（+测试）：路由和错误映射。
- `docs/goofish-endpoints.md`：本文件。

改动的上游文件（只加装配行）：

- `internal/server/application_ports.go`：`ApplicationPorts`/`ApplicationPortsInput` 增加可选的 `goofishItems`（不在必需校验里）。
- `internal/server/versioned_session_routes.go`：挂载 `mountVersionedGoofishItems`。
- `internal/composition/application_services.go`、`application_services_items.go`、`runtime/server_dependencies.go`：把 `GoofishItemsPort` 传到 Server。
- `api/openapi.yaml`：登记 5 个 operation 和对应 schema（上游有路由与 OpenAPI 双向一致的测试）。
- `internal/server/openapi_response_contract_test.go`：成功契约场景表加一行。

上游 v1.0.14 没有按账号节流器（main 上后来才有 `pacer.go`），所以本分支在适配器里自带一个；rebase 到有节流器的上游版本时可以换成上游的。

## 待线上验证

以下内容按抓包记录和第三方实现推断，单测只覆盖了载荷构造和错误分类，需要在真实账号上确认：

1. 发布：editDetail 里的 `itemLabelExtList` 原样带进 publish 是否被接受（上游 publish 用的是类目推荐返回的另一种标签格式）。
2. 发布：`itemTextDTO.titleDescSeparate=false` 时卡片标题是否取描述第一行。
3. 编辑：`sourceId` 用 `pcMainPublish` 是否正确（pyxianyu 用 itemId 兜底）；白名单字段是否齐全；编辑已下架商品是否会重新上架。
4. 下架：已下架、不存在时闲鱼实际返回的 ret 文案（目前按关键字识别）。
5. 在售列表：`groupId=58877261` 是否对所有账号有效（上游写死该值）；`itemStatus` 的取值。
6. 风控、Session 失效在这些接口上的实际 ret。
