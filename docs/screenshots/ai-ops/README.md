# AI 运维真实组件 CI 预览

来源：[AI operations UI acceptance](https://github.com/ranxi2001/sub2api/actions/runs/41977567961)，渲染提交 `08bde856042167cfbb5251cafc303a60493d5b6c`。真实 Vue 组件：AIOpsView、AIOpsControls、AIOpsRecoveryProbes、SmartOpsNav；API、账号、模型和证据均为合成数据。

- `disabled-desktop-synthetic.png`：默认未启用，零创建/生产变更。
- `ready-desktop-synthetic.png`：诊断和控制区域。
- `frozen-desktop-synthetic.png`：准确目标/差异/证据/摘要的冻结预览，未确认前不可执行。
- `frozen-mobile-synthetic.png`：390px移动端，无横向溢出。

verification.json记录：无浏览器错误、无生产API、无付费模型请求；明确确认后仅一次精确摘要的合成apply。它证明组件行为，不证明真实模型、PostgreSQL、跨节点或生产部署。当前PR后续提交未改变这些组件布局；最新CI仍独立验证实际head。
