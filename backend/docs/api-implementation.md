# 文档模块四类接口实现说明

本文档依据当前代码库整理，说明前端“文档”模块展示的文本、图片、视频、音频四类接口在后端的真实执行链路。文中所说的“接口”是四个能力分类，并不是只有四个 HTTP 路由。

## 1. 先看结论：前端文档页不负责实现接口

文档页位于 `frontend/src/features/docs/api-docs-page.tsx`。它主要维护一个 `endpoints` 定义表，内容包括：

- 接口分类、标题、HTTP 方法和路径；
- 字段名称、是否必填和国际化说明；
- curl、Python、JavaScript 请求示例；
- 静态的响应示例；
- 根据当前可用模型筛选可展示的接口。

页面还会调用 `getSystemInfo()` 获取公开 API 地址，调用 `listModels()` 获取模型列表，并把公开地址拼成 `publicApiBaseURL + /v1`。因此页面只是接口说明和示例生成器，真正处理请求的是后端 `backend/internal/transport/http`、`backend/internal/application/gateway` 和 `backend/internal/infra/provider` 三层。

## 2. 总体请求链路

四类接口共用下面的主链路：

```text
客户端
  │  Authorization: Bearer <key> 或 X-API-Key
  ▼
HTTP Server /v1
  ├─ 请求体观测、大小限制、并发控制
  └─ ClientAuth：校验客户端 Key、绑定并发租约、写入 Gin Context
  ▼
Inference Handler
  ├─ 校验 Content-Type 和 JSON / multipart / WebSocket 形态
  ├─ 校验字段、默认值和接口特有约束
  └─ 组装 Gateway Input，并读取 request_id、client key
  ▼
Gateway Service
  ├─ 解析公开模型名和 Provider 路由
  ├─ 按 capability、客户端权限和账号范围过滤
  ├─ 选择账号、egress 节点和会话亲和性
  ├─ 预留或结算计费，维护配额和审计
  └─ 调用 Provider Registry 中的能力适配器
  ▼
Provider Adapter
  ├─ 把统一内部请求转换为 Console / Web / Build 协议
  ├─ 处理上游鉴权、出口、轮询、流解析和媒体下载
  └─ 把结果转换为统一 provider.Response / Result
  ▼
Inference Handler
  ├─ 转回 Responses、Chat、Anthropic 或媒体协议
  └─ 在响应读取完成或流关闭时触发最终审计、计费和租约释放
  ▼
客户端
```

入口在 `backend/internal/transport/http/server.go` 注册 `/v1`，随后由 `inference.Handler.Register()` 注册推理路由。`backend/internal/transport/http/middleware/auth.go` 负责客户端 Key 认证；认证失败不会进入 Gateway。

### 2.1 Provider 能力矩阵

Provider 不是通过一个巨大接口实现全部能力，而是通过 `provider.go` 中的多个小接口注册能力。当前代码的能力分布如下：

| 能力 | Console | Web | Build / CLI |
| --- | --- | --- | --- |
| 文本 Responses / Chat / Messages | 支持 | 支持 | 支持 |
| 图片生成 | 支持 | 支持 | 未注册图片适配器 |
| 图片编辑 | 支持 | 支持 | 未注册图片适配器 |
| 视频生成 | 支持 | 支持，当前仅文本生视频 | 支持 |
| 视频编辑、延长 | 支持 `grok-imagine-video` | 不支持 | Gateway 不将 Build 选作编辑 / 延长路由，适配器主要实现视频生成协议 |
| TTS | 支持 | 未注册 | 未注册 |
| STT | 支持 | 未注册 | 未注册 |
| 实时语音 WebSocket | 支持 | 未注册 | 未注册 |

Gateway 只会调用当前 Provider 已注册的能力，不会因为某个账号存在就假设它支持所有接口。

## 3. 文本接口：Responses、Chat Completions 和 Messages

### 3.1 路由

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/v1/responses` | Responses 协议文本请求 |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions 兼容入口 |
| `POST` | `/v1/messages` | Anthropic Messages 兼容入口 |
| `POST` | `/v1/responses/compact` | Responses 压缩 / compact 请求，强制非流式 |
| `GET` | `/v1/responses/{responseId}` | 获取已归属当前客户端的 Response |
| `DELETE` | `/v1/responses/{responseId}` | 删除已归属当前客户端的 Response |

模型文档页展示的 Chat 分类对应 `chat/completions`、`chat/responses`、`chat/messages` 三个页面定义；后端实际还有 compact 和 Response 资源管理路由。

### 3.2 Transport 层处理

实现集中在 `backend/internal/transport/http/inference/handler.go`：

1. `createResponse`、`createChatCompletion`、`createMessage` 分别识别协议并进入统一创建流程。
2. 创建流程要求 `Content-Type: application/json`，用 `MaxBytesReader` 限制请求体，并拒绝多段 JSON。
3. `model` 必须存在；`stream`、`previous_response_id`、`prompt_cache_key` 等字段被提取到 Gateway 输入，同时原始 JSON body 原样保留，供后续协议转换和上游转发使用。
4. 从 Gin Context 读取认证阶段写入的客户端 Key 和 request ID。没有有效 Key 时返回 `invalid_api_key`。
5. compact 请求会把 `stream` 强制改成 `false`，然后调用 `Gateway.CompactResponse()`；其他请求调用 `Gateway.CreateResponse()`。

### 3.3 Gateway 的统一处理

`backend/internal/application/gateway/service.go` 的 `CreateResponse`、`CompactResponse` 最终进入 `createResponseAt()`。这个方法把三种公开协议统一纳入同一套路由和治理流程：

1. 创建审计事件、请求作用域和 egress trace。
2. 解析公开模型名、Provider 前缀、reasoning alias，并找到对应的模型路由。
3. 如果有 `previous_response_id`，读取已保存的 Response 状态，并校验该 Response 属于当前客户端和对应账号范围。
4. 按 `ResponseAdapter` 能力、客户端允许的 Provider、模型权限、账号范围和模型可用状态筛选候选路由。
5. 根据 Provider 优先级、账号配额、冷却状态以及 rendezvous hash / 会话亲和性选择目标账号。
6. 对 Build 请求建立 prompt cache/session identity；在合适的模型和请求条件下处理 reasoning replay。
7. 根据官方价格预留文本计费额度。
8. 调用 `providers.Responses(provider)` 返回的 `ResponseAdapter.ForwardResponse()`。
9. 对 SSO 失效、403、429、5xx、网络错误等情况按错误类型更新账号状态，并在策略允许时切换账号或出口重试。
10. 返回带最终化回调的 `gateway.Result`。响应流真正关闭后，才释放账号租约并写入 token、首 token 时间、失败尝试、计费和配额记录。

Chat Completions 和 Anthropic Messages 的公开格式并不要求上游也使用相同协议。它们在统一请求阶段进入 Responses 线路，再由具体 Provider 或 conversation 转换器负责请求和响应协议适配。

### 3.4 三个文本 Provider 的实现差异

#### Build / CLI

`backend/internal/infra/provider/cli/adapter.go` 的 `ForwardResponse()` 是 Build 文本主适配器，主要处理：

- Chat / Messages 到 Build Responses 请求的转换；
- Responses body 标准化、工具兼容和 reasoning 参数；
- prompt cache key 注入和 cache 路由；
- reasoning replay 的捕获与恢复；
- gzip 响应解压、流式语义空闲超时；
- Build 主地址返回明确 403 时，对符合条件的 Super 账号尝试 XAI fallback；
- 上游成功后再按公开协议恢复为 Responses、Chat 或 Anthropic 格式。

#### Console

`backend/internal/infra/provider/console/adapter.go` 的 `ForwardResponse()` 使用 Console 的 DPoP 会话和 Console egress。它把内部统一的文本请求转换为 Console Responses 形态，并处理账号凭据、出口反馈、HTTP 错误、流和非流响应，再交回 Gateway / Transport 层完成公开协议输出。

#### Web

`backend/internal/infra/provider/web/chat.go` 使用 Grok Web 的私有会话 / 流协议。它负责建立 Web conversation、解析 Web 流事件、提取可见文本和工具调用，并把结果转换成 OpenAI 或 Anthropic 的事件流 / JSON。Web 流中还包含搜索、思考阶段和图片附件等私有事件，适配器会只暴露目标公开协议需要的内容，并在需要时保存 Response 状态和会话信息。

### 3.5 输出和异常

`writeProtocolResult()` 统一输出结果：

- 非流式成功响应会先确认上游至少返回一个非空 JSON；
- 非流式 JSON 传输上限约为 128 MiB，流式传输上限约为 256 MiB；
- 根据 Responses、Chat、Anthropic 三种协议复制或转换 JSON / SSE；
- 对上游 credential 状态、空响应、空闲超时、流中断、流失败终止事件、响应过大分别映射公开错误码；
- 流关闭后触发 `Result.Finalize()`，因此即使客户端提前断开，也能记录 `client_stream_interrupted` 等结果。

## 4. 图片接口：生成和编辑

### 4.1 路由和输入约束

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| `POST` | `/v1/images/generations` | 根据 prompt 生成图片 |
| `POST` | `/v1/images/edits` | 根据一张或多张输入图片和 prompt 编辑图片 |

图片请求在 `handler.go` 中只接受 JSON，不接受 multipart：

#### 图片生成

- `model`、`prompt` 必填；
- `n` 默认 1，范围 1 到 10；
- `stream=true` 时只能请求 `n=1`；
- `partial_images` 范围为 0 到 3，且只有流式请求可以使用；
- `quality` 只接受 `low` / `medium`；
- `aspect_ratio`、`size`、`resolution` 等参数按统一兼容层规则校验；
- `storage_options` 当前明确返回不支持，而不是静默忽略。

#### 图片编辑

- `model`、`prompt` 必填；
- 输入字段可以是 `image` 或 `images`，合并后必须有 1 到 8 张；
- 每张图片当前必须提供 `url`，`file_id` 暂不支持；
- `n` 范围 1 到 10；
- `partial_images` 同样只能用于流式请求，范围 0 到 3；
- `aspect_ratio` 使用统一比例白名单；`size` 支持 `auto`、`1024x1024`、`1024x1536`、`1536x1024`；
- `resolution` 默认 `1k`，只接受 `1k` / `2k`；
- `quality` 只接受 `low` / `medium`。

校验完成后，Handler 将请求组装为 `gateway.ImageGenerationInput` 或 `gateway.ImageEditInput`，并携带原始方法、路径和请求头用于审计。

### 4.2 Gateway 执行流程

`backend/internal/application/gateway/image.go` 以 `executeImage()` 收口生成和编辑：

1. 通过 capability 区分 `image` 和 `image_edit`，只保留实现了 `ImageGenerationAdapter` 或 `ImageEditAdapter` 的 Provider。
2. 根据客户端 Key 的 Provider 范围和模型权限继续过滤路由。
3. 选择可调度账号和 egress，并检查账本是否可用。
4. 按模型、分辨率、质量、生成数量和编辑输入数量预估图片费用，并预留客户端余额。
5. 调用 `ImageGenerationAdapter.GenerateImage()` 或 `ImageEditAdapter.EditImage()`。
6. 对 SSO 失效、403、429、远程配额窗口等情况更新账号 / 配额状态，并按重试策略重新选择账号。
7. 返回 `provider.Response`。响应被读取或流被关闭后，最终化回调才写入成功 / 失败审计、输出图片数量、实际价格和配额变化。

### 4.3 Console 图片实现

`backend/internal/infra/provider/console/media.go` 直接对应 Console 的：

- `/images/generations`；
- `/images/edits`。

请求通过 DPoP 和 Console egress 发出，响应中的图片 URL 会经过信任域校验，并使用同一账号的媒体出口下载。对于 URL 响应，适配器会：

1. 解析 `data` 数组并验证每一项都有 URL；
2. 下载上游临时图片；
3. 校验 Content-Type、大小和响应内容；
4. 调用媒体服务保存图片；
5. 把上游 URL 替换成项目自己的 `/v1/media/images/{asset_id}` 地址。

这样客户端不会依赖可能过期的 Console 临时 URL。下载和落盘失败被包装成媒体 post-processing 错误，Gateway 不会把它误记为普通模型选择失败。

### 4.4 Web 图片实现

`backend/internal/infra/provider/web/image.go` 根据模型走不同实现：

- `imagine-lite` 路径按请求数量生成图片 URL；
- 普通 Imagine 路径建立 WebSocket，发送 reset / 生成消息；
- collector 从 Web 私有事件中收集预览图、最终图和图片 URL；
- 流式请求可以输出有限数量的 partial image 事件；
- 最终图片下载后统一保存到媒体服务；
- `response_format=url` 返回本地媒体 URL，`response_format=b64_json` 返回 Base64 数据项。

图片编辑会先下载输入图片，再通过 Web 的 Direct File Upload 上传，生成 `mediaGenInput.imageToImage` 请求，解析私有流并归档最终图片。Web clearance 错误最多重试一次，避免无限重放生成请求。

### 4.5 图片媒体资源生命周期

`backend/internal/application/media/service.go` 的 `SaveImage()` 是图片归档的统一入口：

1. 限制大小并用 `http.DetectContentType()` 检测真实 MIME；
2. 只允许支持的图片类型；
3. 生成不可预测 asset ID，计算 SHA-256；
4. 先写入对象存储，再写入媒体资产元数据；
5. 元数据失败时删除已经写入的对象，避免半成品；
6. 达到容量阈值后触发后台清理。

`backend/internal/transport/http/media/handler.go` 通过 `/v1/media/images/{assetId}` 提供 GET / HEAD。永久图片使用不可猜测 ID、SHA-256 ETag、长缓存和 `nosniff`。临时输入资产不会通过该公共路由暴露。

## 5. 视频接口：生成、编辑、延长和查询

### 5.1 路由

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| `POST` | `/v1/videos/generations` | 创建视频生成任务 |
| `POST` | `/v1/videos/edits` | 创建视频编辑任务 |
| `POST` | `/v1/videos/extensions` | 创建视频延长任务 |
| `GET` | `/v1/videos/{request_id}` | 查询任务状态和结果 |
| `GET` | `/v1/videos/{request_id}/content` | 认证后读取视频二进制 |

视频创建接口都是 JSON，并且解码时拒绝未知字段。创建接口返回：

```json
{
  "request_id": "video_xxx"
}
```

### 5.2 Handler 参数校验

#### 生成

- `model` 必填；
- `duration` 默认 8 秒，接受整数或整数字符串，范围 1 到 15 秒；
- `aspect_ratio` 默认 `16:9`，支持 `1:1`、`16:9`、`9:16`、`4:3`、`3:4`、`3:2`、`2:3`；
- `resolution` 默认 `720p`，支持 `480p`、`720p`、`1080p`；
- `image` 表示首帧图；
- `reference_images` 表示参考图；
- `reference_audios` 使用 `voice_id`，最多 3 个；
- `image` 不能和 `reference_images` / `reference_audios` 同时使用；
- 参考图或参考音频模式必须有 prompt，并且最高 720p；
- 纯文本生视频必须有 prompt；图片生视频可以省略 prompt；
- 生成接口不接受 `video`；
- `output.upload_url` 和 `storage_options` 当前不对外支持。

图片 / 视频输入对象必须且只能提供一个 `url` 或 `file_id`。`file_id` 必须是系统认可的临时输入资产 ID，Handler 会把它编码为内部引用，防止把内部资源语义直接暴露给 Provider。

#### 编辑

- 必须同时提供 prompt 和 video；
- 只接受 `video` 作为媒体输入；
- 不允许 image、reference_images、reference_audios、aspect_ratio、resolution；
- 不允许 duration；
- Gateway 会进一步把路由限制到 Console 的 `grok-imagine-video`。

#### 延长

- 必须同时提供 prompt 和 video；
- 不允许 image、reference_images、reference_audios、aspect_ratio、resolution；
- `duration` 默认 6 秒，范围 2 到 10 秒；
- 同样只使用 Console `grok-imagine-video` 的编辑 / 延长能力。

### 5.3 Gateway：先持久化，再异步执行

`backend/internal/application/gateway/video.go` 的 `CreateVideo()` 不会在 HTTP 请求中同步等待生成完成，而是：

1. 校验操作类型和 prompt 长度；
2. 按生成 / 编辑 / 延长筛选路由和 Provider；
3. 按 Provider 差异校验分辨率、参考图数量、模型和时长；
4. 验证 URL 或内部临时资产引用；
5. 把操作类型和所有输入编码到持久化的 `InputJSON`；
6. 选择账号并记录首选 `AccountID`；
7. 预留视频费用；
8. 创建 `media.Job`，初始状态为 `queued`；
9. 把任务 ID 放入有限容量队列；
10. 立即向客户端返回 `request_id`。

这使得进程重启后可以从数据库恢复任务，而不是依赖只存在于某个 goroutine 内的状态。

### 5.4 视频任务状态机

```text
创建请求
   │
   ▼
queued ── worker claim / lease ──▶ in_progress
                                      │
                         ┌────────────┴────────────┐
                         ▼                         ▼
                    completed                   failed
                    progress=100                 保存错误码和消息
```

实现细节：

- `RunVideoWorkers()` 使用固定数量 worker 和有限队列，避免每个请求创建无界 goroutine；
- 任务有约 2 小时执行超时和独立 lease，防止同一任务被多个实例重复处理；
- `RunVideoRecovery()` 每 30 秒扫描未启动或执行实例失联的任务；
- 首次调用固定使用创建任务时选中的账号；创建阶段失败时，按策略可以切换账号；
- 一旦上游已经提交，后续 poll / 下载 / 后处理不会随意换账号，避免重复生成；
- 成功后写入上游 URL、Content-Type，或者写入本地 `ResultAssetID`；
- 终态会扣减 / 确认配额、完成审计，并释放不再被活动任务引用的临时输入资源。

### 5.5 查询和视频内容读取

`GET /v1/videos/{request_id}` 会先校验任务属于当前客户端，然后按状态返回：

- `queued` / `in_progress`：`status=pending`，同时返回模型和当前进度；
- `completed`：`status=done`、进度 100 和 `video.url`；生成任务会带 duration；
- `failed`：`status=failed`，错误码被映射成公开的 `service_unavailable`、`invalid_argument` 或 `internal_error` 等。

播放 URL 优先级如下：

1. 如果有本地 `ResultAssetID`，返回 `/v1/media/videos/{asset_id}`，适合浏览器或播放器直接读取；
2. 没有本地资源时返回受客户端 Key 保护的 `/v1/videos/{request_id}/content`。

`/content` 会再次验证任务归属，调用 `VideoContentDownloader` 使用创建该任务的账号下载上游视频，只允许 `video/mp4`、`video/quicktime`、`video/webm`，并限制最大约 2 GiB。媒体响应设置 `Content-Disposition`、`nosniff`、私有缓存策略；未知长度的流会通过 trailer 报告传输中断。

### 5.6 Console 视频

`console/media.go` 根据操作调用：

- `/videos/generations`；
- `/videos/edits`；
- `/videos/extensions`。

适配器使用 DPoP 和 Console egress 创建任务，再按约 2 秒间隔轮询 `/videos/{id}`。它在 Provider 层再次校验模型、时长、参考图数量、1080p 适用模型及编辑 / 延长输入，作为 Gateway 之外的最终出站边界。

完成后返回上游视频 URL。后续下载会检查可信域名、重定向目标、Content-Type，并复用对应账号的 Console 资产 egress。

### 5.7 Web 视频

`web/video.go` 目前只接受文本生视频；如果带首帧图或参考图，会在 prepare 阶段拒绝，并提示使用 Build 或 Console。

实现使用 Grok Web 私有 `/rest/app-chat/conversations/new`：

1. 参数放在 `mediaGenInput.textToVideo`；
2. 生成请求带模型、prompt、比例、时长和分辨率；
3. 同时兼容 SSE 和 JSON 流；
4. 从 `streamingVideoGenerationResponse` 或附件字段提取视频 URL；
5. Basic Web 账号会应用视频时长上限；
6. 成品下载必须复用生成账号的 Web 资产会话。

### 5.8 Build / CLI 视频

`cli/video.go` 对外使用 `grok-imagine-video-1.5`，默认走 Build OAuth 视频接口并轮询任务。

- 显式 XAI 模式，或符合条件的 Super + bot flag 账号，可以直接使用 XAI 路径；
- Build 创建返回 403 时，符合条件的账号可以探测 XAI；
- XAI 路径先向媒体服务申请一次性视频上传票据，将 `output.upload_url` 发送给 XAI；
- XAI 完成后等待本项目的 PUT 上传接收端，优先返回本地媒体资产；
- 若本地上传未到达但状态中仍有可信 CDN URL，可以回退到远程 URL；
- Build CDN 下载是匿名 GET，不携带 OAuth、Token-Auth 或客户端身份头。

### 5.9 视频输入资产和上传票据

`backend/internal/application/media/service.go` 和 `transport/http/media` 提供两类辅助能力：

- 管理端 `/api/admin/v1/media/inputs/import`：服务端抓取管理员提供的图片 URL；
- 管理端 `/api/admin/v1/media/inputs/upload`：接收管理员上传的图片或视频；
- 公开 `/v1/media/uploads/{token}`：只使用一次性票据接收 XAI 视频 PUT；
- 公开 `/v1/media/videos/{asset_id}`：读取已经归档的最终视频。

临时输入资产使用特殊 ID、默认 20 MiB 单文件上限和 24 小时硬 TTL，不进入公开图片 / 视频图库。任务结束后，如果没有其他活动任务引用，Gateway 会立即释放；后台 Cleanup 仍会兜底清理过期对象。

管理员 URL 导入有额外 SSRF 防护：只允许无用户凭据的 HTTP / HTTPS 80 / 443 地址；DNS 解析结果必须是公网地址；不自动跟随重定向，而是每一跳重新解析和校验，最多 5 跳。

## 6. 音频接口：TTS、STT 和实时语音

音频与图片、视频最大的差异是：TTS 默认直接返回二进制，STT 返回文本 JSON，而实时接口是双向 WebSocket。当前音频实现实际落到 Console Voice Provider。

### 6.1 路由

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| `POST` | `/v1/tts` | 原生 TTS |
| `GET` | `/v1/tts/voices` | 列出声音 |
| `GET` | `/v1/tts/voices/{voice_id}` | 查询单个声音 |
| `POST` | `/v1/stt` | 原生 STT |
| `GET` + WebSocket Upgrade | `/v1/stt` | 流式 STT |
| `POST` | `/v1/audio/speech` | OpenAI TTS 兼容入口 |
| `POST` | `/v1/audio/tasks` | 兼容部分客户端的 TTS 入口，行为与 speech 相同 |
| `POST` | `/v1/audio/transcriptions` | OpenAI STT 兼容入口 |
| `GET` + WebSocket Upgrade | `/v1/realtime` | 实时语音 WebSocket |

### 6.2 原生 TTS Transport

`backend/internal/transport/http/inference/voice_handler.go` 的 `synthesizeSpeech()`：

1. 只接受 JSON，并限制请求体大小；
2. `text` 和 `language` 必填；
3. `model` 默认 `grok-voice-latest`；
4. 解析 `output_format`、`speed`、`optimize_streaming_latency`、`text_normalization` 和 `with_timestamps`；
5. 组装 `gateway.TTSInput` 并调用 Gateway；
6. 没有时间戳时直接写原始音频 bytes；
7. `with_timestamps=true` 时返回包含 Base64 音频和时间戳信息的 JSON envelope。

校验规则包括：`speed` 范围 0.25 到 4.0，`optimize_streaming_latency` 必须是 0 到 4 的整数，`output_format` 必须是合法的结构化格式。

### 6.3 OpenAI TTS 兼容

`openai_audio_handler.go` 将 OpenAI 请求转换成同一个 `gateway.TTSInput`：

| OpenAI 字段 | 内部字段 |
| --- | --- |
| `input` | `text` |
| `voice` | `voice_id` |
| `language` | `language`，缺省为 `en` |
| `response_format` | `output_format.codec` |
| `speed` | `speed` |

支持 `mp3`、`opus` / `ogg`、`aac`、`flac`、`wav`、`pcm`。常见 OpenAI voice 会映射到 Console voice：

| OpenAI voice | Console voice |
| --- | --- |
| `alloy`、`verse` | `ara` |
| `echo`、`ballad` | `eve` |
| `fable`、`coral` | `sal` |
| `onyx`、`ash` | `rex` |
| `nova`、`sage` | `leo` |
| `shimmer`、`marin` | `sia` |

未知 voice 会原样传递，便于使用自定义或原生 Grok voice ID。`/audio/speech` 和 `/audio/tasks` 默认返回原始音频二进制；只有显式要求时间戳或 JSON envelope 时才返回 JSON。

### 6.4 STT Transport

原生 `/v1/stt` 和 OpenAI `/v1/audio/transcriptions` 共用 `transcribeSpeechRequest()`，支持：

- `multipart/form-data`：`file` 或 `url`、model、language、audio format、采样率、声道、diarize、keyterm、filler_words、VAD 等；
- `application/json`：适合直接传 URL 和同类选项；
- 输入必须提供 file 或 URL；
- OpenAI 兼容输出支持 `json`、`verbose_json`、`text`。

OpenAI 不支持且无法无损转换到 Console STT 的参数（例如非零 temperature、prompt、timestamp granularities）会明确返回 `unsupported_parameter`，不会静默丢弃客户端语义。STT 结果可包含文本、语言、duration、词级时间戳和其他上游 JSON 字段。

### 6.5 Voice Gateway

`backend/internal/application/gateway/voice.go` 对 TTS / STT 做统一治理：

- TTS 按输入文本预估费用；
- 按模型 capability 筛选支持 TTS 或 STT 的 Provider；
- 选择账号和 egress，调用 `TTSAdapter.SynthesizeSpeech()` 或 `STTAdapter.TranscribeSpeech()`；
- 根据账号、Provider 和上游 HTTP 状态决定重试或失败；
- 成功后记录音频输出或识别结果、耗时、计费和配额；
- 原始音频走二进制响应，带时间戳的 TTS 走 JSON Base64 envelope；
- STT 根据 `text`、`json`、`verbose_json` 选择输出格式。

### 6.6 Console Voice Provider

`backend/internal/infra/provider/console/voice.go` 是当前音频实际出站适配器：

- TTS 调用 Console `/tts`，解析原始音频或 JSON Base64 envelope；
- 读取 `audio`、`content_type`、`duration`、`audio_timestamps` 等字段；
- voice 列表和单个 voice 查询调用 Console `/tts/voices`；
- STT 将 file / URL 与识别选项重新组装成 multipart，再调用 Console `/stt`；
- 对音频响应设置安全大小上限，避免异常上游导致内存或传输失控。

### 6.7 实时语音 WebSocket

实时入口在 `voice_ws_handler.go`，Gateway 入口在 `gateway/voice_ws.go`，Console 拨号在 `console/voice_ws.go`。

连接过程如下：

1. Handler 检查客户端请求是否包含有效 WebSocket Upgrade；普通 HTTP 请求会直接返回错误。
2. Gateway 根据路径把 `/realtime` 映射为 `realtime` capability，把 `/stt` 映射为 `stt` capability，并选择支持 `VoiceWebSocketAdapter` 的 Console 账号。
3. `console.DialVoiceWebSocket()` 解密账号 token，申请 Console egress，创建 DPoP 证明，并把 HTTP 证明端点转换为 `wss` / `ws` 拨号地址。
4. 上游连接成功后，Handler 再升级客户端连接。
5. 两个 goroutine 进行客户端到上游、上游到客户端的双向消息转发。
6. 两侧单条消息读取上限为 16 MiB；任意一侧异常会关闭双方连接并执行一次 Finalize。
7. STT 转发过程中识别 `transcript.done` 事件，提取音频 duration 用于审计和计费。

实时会话的账号租约会保持到 WebSocket 结束，而不是在握手完成后立即释放。最终审计区分客户端中断、上游中断和成功结束；成功的 STT 还会按音频时长计算官方计费信息。

## 7. 公共媒体和安全边界

### 7.1 永久资源与临时输入分离

媒体服务把输出资源和输入资源区分开：

- 输出图片 / 视频：无过期时间，写入对象存储和元数据，可通过公开的不可猜测 asset ID 读取；
- 视频 / 图片输入：带 `ExpiresAt`，只能由 Gateway 内部用 `OpenInputAsset()` 读取，不通过公开媒体路由暴露；
- 任务进入终态后，`ReleaseInputAssets()` 只回收没有其他活动任务引用的输入；
- Cleanup 在跨实例锁保护下清理过期输入、过期票据和超出容量阈值的旧输出。

### 7.2 URL 和内容校验

- Console 图片 / 视频下载只允许配置的可信上游域名，并检查重定向最终地址；
- Web 资产下载要求 HTTPS 和可信资产域；
- Build CDN 下载不携带 OAuth 或客户端 Token；
- 媒体输出的 Content-Type 必须符合图片 / 视频白名单；
- 视频内容最大传输约 2 GiB，文本流和 JSON 响应也有独立上限；
- 管理端 URL 图片导入通过 DNS、公网 IP、端口、重定向次数和连接阶段控制防止 SSRF。

### 7.3 权限、审计和计费

- 客户端 Key 决定可用模型、Provider 和账号范围；
- Response、视频任务等资源查询会检查客户端归属；
- 图片和视频最终资源使用不可猜测 ID；
- XAI PUT 上传使用一次性上传票据，不要求客户端携带 API Key；
- 计费通常先预留、成功后确认，失败或未进入最终化时释放预留；
- 账号租约、配额窗口、egress 节点反馈和审计记录都在 Gateway / WebSocket Finalize 阶段收口。

## 8. 关键源文件索引

| 层次 | 文件 | 主要职责 |
| --- | --- | --- |
| 前端文档 | `frontend/src/features/docs/api-docs-page.tsx` | 四类接口定义、字段说明、示例和模型筛选 |
| HTTP 入口 | `backend/internal/transport/http/server.go` | `/v1`、中间件和服务初始化 |
| 客户端认证 | `backend/internal/transport/http/middleware/auth.go` | Bearer / X-API-Key、客户端 Key 和并发租约 |
| 推理路由 | `backend/internal/transport/http/inference/handler.go` | 文本、图片、视频路由、参数校验和输出 |
| 音频路由 | `backend/internal/transport/http/inference/voice_handler.go` | 原生 TTS / STT |
| OpenAI 音频兼容 | `backend/internal/transport/http/inference/openai_audio_handler.go` | speech / tasks / transcriptions 适配 |
| 实时语音 | `backend/internal/transport/http/inference/voice_ws_handler.go` | 客户端与上游 WebSocket 双向转发 |
| 文本 Gateway | `backend/internal/application/gateway/service.go` | Responses 统一路由、协议、计费和审计 |
| 图片 Gateway | `backend/internal/application/gateway/image.go` | 图片路由、计费、重试和最终化 |
| 视频 Gateway | `backend/internal/application/gateway/video.go` | 任务持久化、队列、worker、轮询和恢复 |
| 音频 Gateway | `backend/internal/application/gateway/voice.go` | TTS / STT 路由和媒体结果输出 |
| 实时语音 Gateway | `backend/internal/application/gateway/voice_ws.go` | WebSocket capability、账号租约和审计 |
| Provider 抽象 | `backend/internal/infra/provider/provider.go` | 各能力适配器接口和 Registry |
| Console 文本 | `backend/internal/infra/provider/console/adapter.go` | Console Responses 出站适配 |
| Console 图片 / 视频 | `backend/internal/infra/provider/console/media.go` | 图片、本地化、视频创建 / 轮询 / 下载 |
| Console 音频 | `backend/internal/infra/provider/console/voice.go` | TTS、STT 和 voice 列表 |
| Console 实时语音 | `backend/internal/infra/provider/console/voice_ws.go` | DPoP 认证的上游 WebSocket |
| Web 文本 | `backend/internal/infra/provider/web/chat.go` | Web 私有会话和流转换 |
| Web 图片 | `backend/internal/infra/provider/web/image.go` | Imagine / image-to-image / 图片归档 |
| Web 视频 | `backend/internal/infra/provider/web/video.go` | 文本生视频、SSE / JSON 解析和资产下载 |
| Build 文本 | `backend/internal/infra/provider/cli/adapter.go` | Build Responses、cache、reasoning 和 fallback |
| Build 视频 | `backend/internal/infra/provider/cli/video.go` | Build / XAI 视频创建、轮询和上传 |
| 媒体服务 | `backend/internal/application/media/service.go` | 资源校验、对象存储、元数据和清理 |
| 媒体 HTTP | `backend/internal/transport/http/media/handler.go` | 公开媒体读取、上传票据和管理端媒体接口 |
| 媒体导入 | `backend/internal/transport/http/media/ingest.go` | 图片 / 视频临时输入和 SSRF 安全导入 |

## 9. 一句话总结

四类接口的共同模式是“Handler 做协议和参数边界，Gateway 做模型 / 账号 / 配额 / 计费治理，Provider 做上游协议适配”。其中文本是同步或流式响应链路，图片是同步返回但会把上游图片本地化，视频是持久化异步任务，音频则根据接口形态分别返回二进制、JSON 或双向 WebSocket。
