# WS 客户端通过 HTTP 完整历史续接

部分兼容供应站可完成完整历史的 HTTP 请求，却不能继续使用它刚返回的 previous_response_id。账号的既有 http_bridge 模式可在同一客户端 WS 连接中收集已交付的输入、输出及工具调用，给下一轮补齐历史再转为 HTTP。配置前应对供应链执行合成完整工具回合，不能仅凭 HTTP 200 或模型列表判断可用。

HTTP 没有 WS 的 generate=false 预热语义。现在 OpenAI API-key 桥接对第一帧 `generate:false` 的空输入（缺省、null 或 []） 在网关本地回复空的 response.created / response.completed；不调用上游、不记录用量、不登记 provider response owner。该本地 response ID 只在当前 socket 的紧接下一帧生效，解析后删除；未知或跨连接的指针继续执行既有校验。

预热参数最多保留 8 MiB，下一帧缺省字段继承这些参数，显式字段优先，generate/input/旧指针等不作为默认值继承；展开后仍受客户端消息大小上限约束。非初始预热、非空输入或携带已有 conversation/prompt/previous_response_id 的预热明确拒绝，不因删除 generate 而意外发起付费生成。

这不授权重放已发送的失败回合。原生加密历史、工具交付 proof 与账号/模型/身份边界仍然校验；无法证明完整历史的旧会话不能靠切换协议恢复。启用 HTTP bridge 之前的 WS 连接也不能据此被视作已经迁移。

回归覆盖：空预热后两次实际生成、继承工具 schema/指令与大整数、完整工具调用/结果关联、原始 assistant 历史保留、两次实际用量、本地锚点不写入 owner store，以及非空/重复/外来锚点的拒绝边界。真实供应链与生产验收应单独记录。
