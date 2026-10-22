export default {
  safetyPromptAudit: {
    nav: '安全提示词',
    title: '安全提示词审计',
    description: '查看安全提示词插件记录的模型疑似命中事件。记录仅代表精确命中，尚未人工确认；插件不提供本站用户身份。',
    refresh: '刷新',
    openPlugins: '插件管理',
    loading: '正在读取插件状态…',
    loadFailed: '读取插件审计失败，请重试。',
    pluginMissing: '未安装安全提示词插件',
    pluginMissingHint: '请先在插件管理中安装并启用 local.sub2api.safety-prompt，再刷新本页。',
    pluginDisabled: '插件当前未运行，无法读取审计事件。',
    pluginState: '插件状态',
    version: '版本',
    storageReady: '持久保存正常 · 保留 {days} 天 / 最近 {limit} 条 · 因容量淘汰 {evictions} 条',
    storageWarning: '存储告警：{warning}',
    noAudit: '插件未返回审计数据。请确认插件已启用，或打开插件管理查看完整状态。',
    filters: {
      from: '起始日期（本地时间）',
      to: '结束日期（含当天）',
      search: '搜索事件 / 请求编号 / 模型 / 上游账号 / 摘要',
      all: '全部保留记录',
      today: '今天'
    },
    count: '显示 {shown} / {total} 条。均为模型疑似命中，尚未人工确认。',
    badRange: '起始日期不能晚于结束日期。',
    empty: '没有符合条件的记录。',
    table: {
      time: '时间 / 事件编号',
      model: '模型 / 上游账号',
      user: '请求用户',
      summary: '脱敏摘要'
    },
    unknownUser: '未知（插件未提供）',
    requestId: '请求编号',
    upstreamAccount: '上游账号',
    exportJson: '导出 JSON',
    exportCsv: '导出 CSV',
    exportEmpty: '当前没有可导出的记录。',
    eventId: '事件编号',
    model: '模型',
    action: '动作',
    summary: '摘要',
    noSummary: '无文本摘要',
    requestSha: '注入前请求 SHA-256'
  }
}
