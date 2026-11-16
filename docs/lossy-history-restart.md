# 有损会话续接

仅在明确接受上下文丢失时启用：

```yaml
gateway:
  openai_ws:
    lossy_history_restart_enabled: true
```

还需已有管理设置 `openai_409_omit_encrypted_history_enabled=true`。默认关闭，HTTP Responses 与 WS 共用；不改变普通新文本请求。

开启后，在发送前将带供应商状态的请求重新整理为可读文本：移除 previous_response_id、conversation、不可读 agent/reasoning/compaction、媒体及未知输入；保留可读消息和摘要。工具调用与结果仅作为用户提供的未验证数据，不能形成工具执行或归属证明。只有密文时明确提示任务内容已丢失，请求模型索取任务，不冒充恢复成功。当前工具定义、instructions、模型、计费、权限和审核继续执行。

此模式主动整理含状态的每轮请求，可能丢失依赖上游指针的历史、图像等输入，降低回答质量。它不自动重放已发送生成，也不删除服务端归属记录。关闭开关只影响后续请求，无法还原已经丢弃的内容。

新建跨供应商 Agent 任务建议使用 Classic V1 的客户端目录与配置；已有 V2 密文任务不能靠配置还原。该模式是针对 Sub2API 原生 WS/HTTP 的有损兼容处理，不是 CC-Switch 原生 WS 支持，也不代表全部上游错误都能消除。
