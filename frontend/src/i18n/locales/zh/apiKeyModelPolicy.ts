export default {
  title: '密钥模型设置',
  scopeHint: '仅管理员可设置，只对当前 API Key 生效；与密钥的其他信息分别保存。',
  modelMapping: '模型映射',
  mappingHint: '对文本请求精确匹配模型 ID，使用目标模型路由和计费；请求模型和目标模型都需在分组允许范围内。未匹配的模型保持原样，最多添加 100 条。',
  noMappings: '尚未设置映射，请求使用原模型 ID。',
  requestedModel: '请求模型',
  targetModel: '目标模型',
  addMapping: '添加映射',
  removeMapping: '删除映射',
  reasoningEffort: '固定推理强度',
  followClient: '跟随客户端',
  effortHint: '覆盖 OpenAI 请求中的推理强度，客户端未传时也生效。超过分组上限时，按分组策略降级或拒绝请求；请选择目标模型支持的档位。',
  invalidModel: '模型 ID 长度需为 1–200 个 UTF-8 字节，且不含空白或控制字符。',
  duplicateModel: '请求模型“{model}”存在重复映射。',
  tooManyMappings: '最多设置 100 条模型映射。',
  saved: '密钥模型设置已保存',
  saveFailed: '密钥模型设置保存失败'
}
