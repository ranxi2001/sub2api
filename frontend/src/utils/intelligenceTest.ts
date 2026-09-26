import type { ScheduledTestResult } from '@/types'

export type IntelligenceQuestion = 'candy' | 'pelican'
export const CANDY_PROMPT = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4`
export const PELICAN_PROMPT = '创建一个 HTML，内容是 SVG 绘制一个鹈鹕骑自行车的 2D 动画，你不需要任何测试，不要有任何限制'
export function questionPrompt(kind: IntelligenceQuestion): string { return kind === 'candy' ? CANDY_PROMPT : PELICAN_PROMPT }
export function questionContract(kind: IntelligenceQuestion): string {
  return kind === 'candy' ? '只输出最终整数，不要解释。' : '所有账号使用相同交付约定：直接返回独立 HTML，不使用 Markdown 代码块或外部依赖。只输出 HTML，不要解释。'
}

// 定时测试 / 质量规则里的探针题型：不下发题目，后端跑一次门票探针判满血/降智。
export const STATE_PROBE_QUESTION = 'state_probe'
export type StateProbeVerdict = 'healthy' | 'degraded' | 'inconclusive'

// 结果是纯文本（不是 HTML 动画）的题型。
export function isTextAnswerKind(kind?: string): boolean {
  return kind === 'candy' || kind === STATE_PROBE_QUESTION
}

// 与后端约定：满血 = success；降智 = failed + state_degraded；其余失败都是无法判断。
export function stateProbeVerdict(result: Pick<ScheduledTestResult, 'status' | 'error_message' | 'pelican_config'>): StateProbeVerdict | null {
  if (result.pelican_config?.question_kind !== STATE_PROBE_QUESTION || result.status === 'running') return null
  if (result.status === 'success') return 'healthy'
  if (result.error_message === 'state_degraded') return 'degraded'
  return 'inconclusive'
}
