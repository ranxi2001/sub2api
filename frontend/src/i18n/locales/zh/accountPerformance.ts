export default {
 column: '性能 · 近{minutes}分钟', title: '账号性能阈值监控',
 tps: '平均 TPS', degradation: '探针轮次降智率 · {minutes}m', ttft: '首字平均值',
 uncollected: '未采集', unavailable: '暂不可用', configure: '配置阈值',
 paused: '已自动暂停', enabled: '每30秒评估阈值',
 min: '低于时暂停', max: '高于时暂停', noBound: '不限制', save: '保存阈值',
 release: '解除此暂停并关闭监控', close: '关闭',
 saved: '阈值已保存', failed: '监控更新失败；版本冲突时请重新加载。',
 invalid: '阈值必须为有限非负数，下限不能大于上限；百分数范围为0–100。',
 semantics: '按实际请求／探针开始时间计算最近{minutes}分钟滚动值。TPS 是每条请求「输出 token ÷ 请求总秒数」的算术平均（包含等待），沿用用量记录口径。首字使用成功流式文本请求的 first_token_ms。仅统计有唯一完整成功耗时证据的请求；未采集、失败不计为0。',
 qualityNote: '降智百分数 = 已完成且明确错误的质量探针轮次 ÷ 已完成且结论明确的轮次 × 100，沿用现有 correct／incorrect 判定；网络失败、不完整、无法判断的轮次不计入。这是探针失败比例，并非全部业务请求的降智率；账号内各模型和推理档合并统计。',
 latchNote: '任一指标严格低于下限或高于上限时，添加此监控独立拥有的调度暂停原因；等于阈值或缺样本不触发。样本恢复／过期、关闭监控及其他账号编辑均不解除暂停。须显式解除，解除时同时关闭本监控；管理员手动调度状态和其他暂停来源仍然生效。',
 samples: '样本：TPS {tps} · 首字 {ttft} · 质量轮次 {quality}',
 audit: '最近监控事件', noEvents: '暂无监控事件', loading: '加载中…',
 ms: '毫秒', configureEvent: '配置阈值', pauseEvent: '自动暂停', releaseEvent: '解除暂停并关闭监控',
 refresh: '重新加载', reason: '触发指标：{reason}', auditUnavailable: '审计记录暂不可用',
 settings: {
  title: '账号性能监控周期',
  description: '全局配置 TPS、首字耗时和探针轮次降智率的滚动统计周期。',
  windowMinutes: '统计周期（分钟）',
  windowHint: '默认 60 分钟，可设置为 1–1440 的整数；所有账号共用此周期。',
  effectHint: '保存后，后续统计与阈值评估使用新周期，无需重启。仍每 30 秒评估一次；更改周期不会解除已有暂停。',
  invalid: '统计周期必须是 1–1440 之间的整数分钟。',
  saved: '监控周期已保存。',
  loadFailed: '监控周期加载失败，请重新加载后再编辑。',
  saveFailed: '监控周期保存失败，请重试。'
 }
}
