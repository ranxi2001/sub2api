# 有损会话续接

仅在明确接受上下文丢失时启用：

```yaml
gateway:
  openai_ws:
    lossy_history_restart_enabled: true
```

还需已有管理设置 `openai_409_omit_encrypted_history_enabled=true`。默认关闭，仅用于满足下述条件的 HTTP Responses 请求；不改变普通新文本请求。WS 全部跳过有损转换，因为后续帧可能省略工具声明和响应指针，并在转发时才继承前一轮工具；不能按当前帧看似纯文本就丢弃会话状态。

此策略只用于没有工具协议状态的文本请求。带有工具声明（包括 additional_tools）、工具调用／结果、tool_search 或 MCP 工具发现结果、MCP 审批记录、compaction_trigger 以及未知输入类型 的请求不进入有损转换，继续使用原有的配对、归属和发送校验。带有 previous_response_id 或 conversation 的增量请求也交给原恢复路径解析，因为缺少本轮工具声明不代表没有继承的工具与任务。具备可验证的已完成历史时，沿用原客户端会话自动续接；无法安全验证时返回明确错误，不把工具记录伪装成 user 文本。原生 remote compaction v2 和 /responses/compact 也不进入此策略，压缩输入与归属上下文保持原状。

对于仍符合条件且不依赖供应商指针的纯文本请求，开启后在发送前移除已识别的不可读 agent/reasoning/compaction 历史；含媒体的请求保持原协议；保留可读消息和摘要。只有密文时明确提示任务内容已丢失，请求模型索取任务，不冒充恢复成功。instructions、模型、计费、权限和审核继续执行。

此模式主动整理含状态的每轮请求，可能丢失依赖上游指针的历史、图像等输入，降低回答质量。它不自动重放已发送生成，也不删除服务端归属记录。关闭开关只影响后续请求，无法还原已经丢弃的内容。

新建跨供应商 Agent 任务建议使用 Classic V1 的客户端目录与配置；已有 V2 密文任务不能靠配置还原。该模式是针对 Sub2API 原生 WS/HTTP 的有损兼容处理，不是 CC-Switch 原生 WS 支持，也不代表全部上游错误都能消除。

WS 混合已完成 producer 历史的恢复仍保留 function/custom 调用的可信交付、归属与 epoch 校验，已完成副作用作为带原类型、call_id 和结果的惰性记录传递。已完成的 tool_search call/output 保留结构和发现的完整 schema，供后续新调用使用；孤立、重复、类型不匹配或未完成的发现记录拒绝续接。保留发现目录不会创建函数或 custom 工具的执行来源证明。MCP 仅保留协议状态，不代表网关执行外部工具。

恢复中的 discovery call 还必须有实际下游完整交付的作用域证明。客户端第一次返回的 catalog 是该已交付 call 的结果，不是外部工具执行事实；网关在原始 input/controls 与最终 wire 校验后，把完整 catalog 摘要以 CAS 一次性绑定到同一 call 证明。只有后续下游完成才提交 receipt，包含生产者、delivery 与 owner/shadow epoch。以后改变 schema/metadata，或使用未知、失效、未完成交付的 call 均拒绝恢复。连接内增量历史也要重查相同证明；显式“客户端自报历史”降级不能绕过 discovery 证明。
