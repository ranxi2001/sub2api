# 单参考图 edits 表单兼容

部分上游的 `/v1/images/edits` 只接收名为 `image` 的单个上传文件。客户端提交 JSON `images[].image_url` 或 multipart `image[]`、`image[0]` 时，这类上游无法读取参考图。

将 `SUB2API_IMAGE_EDIT_FORM_ACCOUNT_IDS` 设为逗号分隔的**上游账号 ID**（例如虚构的 `101,102`），并重启应用进程后，可为指定账号启用转换。默认空值，关闭兼容模式。Docker Compose 的四套配置均透传此变量；修改 `.env` 后需要重新创建应用容器。systemd 或 Kubernetes 部署需自行将变量注入应用进程。

转换必须同时满足：账号类型为 API Key、上游 base URL 主机名严格匹配 `rpm.tosky.io`、端点为 `/v1/images/edits`、账号 ID 在白名单中。OAuth、generations 和未命中白名单的请求继续使用原路径。

- JSON 中唯一的 `images[].image_url` 转为 multipart `image` 文件；`mask.image_url` 同样转为 `mask` 文件。支持 base64 data URL 和公共 HTTP(S) URL；不支持 `file_id`。
- multipart 中唯一的 `image`、`image[]` 或 `image[0]` 上传统一命名为 `image`，保留文件字节、mask、其他字段以及映射后的模型名。
- 多参考图会报错，不会丢弃其他参考图后继续发起编辑。转换失败时不会提交图片编辑 POST，也不会新增自动重试；目前沿用转发错误路径，通常由 handler 返回 502，不能把它解释为上游已收到请求。
- `stream`、`size`、`quality`、`n` 等参数保留。转换不保证上游支持这些参数，也不保证上游按请求尺寸出图。
- data URL 与 multipart 单项限制 50 MiB，输入和转换后的请求体限制 128 MiB。远程下载沿用网关的出站 URL 校验、账户代理、公网主机/每跳重定向限制、60 秒超时及 20 MiB 上限；下载不携带上游 API Key，下载失败信息不会包含引用 URL。远程下载发生在编辑 POST 之前，遵守客户端取消。

响应格式及 URL → `b64_json` 回填沿用现有配置。此开关不调整计费价格、分组、模型权限或数据库。关闭开关并重启即可恢复原请求转发方式，或回退二进制；没有数据库迁移。转换与下载会增加内存占用和请求准备耗时，没有性能提升声明。

来源：用户提供的 `sub2api-image-edit-form-2.20.2.tar.gz`，原补丁基线 `fb75b87cebe8314d9eba171948367451511f9417`。PR 将转换包适配到当前生产源码，并补充实际转发路径的 mock 回归。包内旧版安装脚本与构建/部署步骤不属于本功能运行路径。

测试由 CI 执行：`backend` 下的 `make test-unit` 和 `make test-integration` 包含转换包与 `TestImageEditForm*` 回归，覆盖字段/文件保留、模型映射、开关范围、下载代理与公网标记、拒绝私网和多图、下载错误脱敏。测试使用虚构账号及 mock HTTP 上游，不调用付费图片接口；真实上游和生产效果需另行验收。
