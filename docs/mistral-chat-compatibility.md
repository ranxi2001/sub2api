# Mistral Chat 兼容与历史回传

Mistral 原生 Chat 的 `content` 可以从 thinking 数组，经含首段 text 的过渡数组，变为后续正文字符串。网关将正文放在标准 `content` 字符串中，思考文本放在 `reasoning_content`；原生数组通过 `mistral_content` 扩展保存，以便下一轮完整回传签名、引用和其他字段。

## 扩展编码

新响应（包括非流式 message 和流式 delta）的 `mistral_content` 是 **JSON Lines 字符串**。每行是一个完整原生 content 数组，以换行符结尾。例如 JSON 解码后：

```text
[{"type":"thinking","thinking":[{"type":"text","text":"Thinking"}],"signature":"example","closed":false}]
[{"type":"thinking","thinking":[],"closed":true},{"type":"text","text":"Hello"}]
```

示例签名仅为虚构测试值。网关不会生成签名。每个事件先紧凑 JSON 编码，再加入换行，因此原生文本中的换行不会产生额外记录。OpenAI SDK 的字符串 delta 累积可直接拼接这些记录；嵌套数组、签名和 closed 布尔值不参与 SDK 的递归对象合并。字符串正文阶段只更新标准 `content`。

客户端应保存完整 assistant 消息，包括扩展、工具调用和累积正文。客户端若丢弃未知扩展，只能回传其保留的文本，无法恢复已丢失的原生签名和引用。自行解析旧数组输出的客户端需要兼容新字符串格式。已发生的旧 SDK 数组合并错误或被客户端破坏的历史无法自动修复。

## 历史输入兼容

入站同时接受以前暴露的 `mistral_content` 原生数组和新 JSON Lines 字符串。新格式按记录顺序还原原生块，不递归合并或改写签名、引用、closed 等字段。

如果扩展包含正文 `Hello`，标准 `content` 已累积为 `Hello world`，网关保留原生块并追加一个正文块 ` world`，避免覆盖或重复正文。完整正文与扩展正文相同则直接还原；正文缺失、null 或空字符串时保留扩展中的正文。正文与扩展已有正文冲突、扩展损坏或同时提供冲突的原生数组时，请求明确失败，不静默舍弃历史。

不含扩展的纯字符串、原生数组及工具消息继续使用原有兼容流程。扩展是网关的 Chat 保真协议；Responses / Messages 桥接目前仍按现有规则处理不支持的原生引用，不能视为完整原生历史通道。

## 推理与路由

`reasoning.effort` 的非空值优先于 `reasoning_effort`，出站统一为 Mistral 原生 `reasoning_effort` 并移除 nested `reasoning`。只有最终上游模型恰为 `zai-glm-5-3` 时应用其已确认档位映射：none/minimal → low，medium → high，xhigh → max。用量元数据读取最终出站档位。

复合分组支持明确的 `mistral/` 前缀及 Mistral 原生模型族。显式路由和精确账号映射保持优先，跨平台精确归属冲突继续拒绝。共享 GLM 名称不据此归为 Mistral；`zai-glm-5-3` 需要 `mistral/` 前缀或显式路由/精确账号映射。

Mistral 账号的 Codex 目录使用已保存的上游上下文、推理档位和输入模态快照，图片能力根据该账号的同步元数据确定，不套用其他供应商的模型名默认值。原生 Chat 的 developer 消息经现有适配改为 system，顺序和其他字段保持不变。

## 离线回归依据

- [Mistral reasoning 文档](https://docs.mistral.ai/studio/conversations/reasoning)：流式阶段及多轮原生历史要求。
- [固定 openai-python accumulate_delta 源码](https://github.com/openai/openai-python/blob/f6e8480f9a5b2a4d02b17a1da54a1e57964bc312/src/openai/lib/streaming/_deltas.py)：单测调用仓库内原样保存的源码，仅替换相对导入的两个类型守卫；测试需要 Python 3，无需安装 SDK 或访问网络。源码及 Apache-2.0 许可证保存在 `backend/internal/service/testdata/mistral/`。
