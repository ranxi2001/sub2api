# 千问 Qwen 平台

新增账号和分组时选择“千问 Qwen”（`qwen`）。平台复用 OpenAI 兼容网关，使用 API Key 和 Chat Completions 上游协议；Responses / Messages 入站使用现有协议转换。新建账号默认不限制模型，避免旧目录限制新模型；可手动设置白名单或模型映射。普通请求、流式请求、模型映射、自定义请求头和自定义上游地址沿用兼容网关能力。

默认地址为 `https://dashscope.aliyuncs.com/compatible-mode/v1`。阿里云目前建议使用工作空间专属地址，例如 `https://<WorkspaceId>.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`；填写真实工作空间 ID，并匹配 Key 所属区域。国际区域或供应站使用其实际兼容地址。参考[阿里云 OpenAI 兼容说明](https://www.alibabacloud.com/help/en/model-studio/compatibility-of-openai-with-dashscope)。本次不提供千问 OAuth、Coding Plan、余额查询或原生 Responses 能力声明。

千问账号应绑定千问分组；组合分组应通过显式路由选择 `qwen`，或使用可识别的千问模型名/账号映射。监控、用量、调度和用户平台额度使用独立的平台 ID，不再把新的千问请求记到 OpenAI。模型价格仍由实际渠道价格与模型价卡决定。

已按 OpenAI 创建的账号和分组不会自动迁移。上线后需要由管理员核对并迁移对应配置；分组平台当前不可在编辑窗口修改，可新建千问分组后按授权迁移绑定与客户端 Key。迁移前的新请求仍属于旧平台。历史错误日志不回填；历史用量的平台展示部分依据当前分组/账号，因此迁移历史展示需要单独评估，不能把本 PR 当作历史统计重算。回滚到旧二进制前需处理新建的 `qwen` 配置，旧版本不识别这个平台。
