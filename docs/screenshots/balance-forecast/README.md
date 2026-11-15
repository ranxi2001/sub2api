# 余额耗尽预测移除的界面证据

`Balance forecast removal` CI 的 `balance-forecast-removal-ui-<head SHA>` artifact 提供 `before-email.png`、`after-email.png` 和记录 base/head SHA 的 `manifest.json`。脚本从独立完整源码树运行真实前端与 SettingsView，使用虚构管理员和本地 API fixture；生产登录、邮件发送和真实接口不在截图验收范围内。截图作为 CI 附件，不提交图片到源码仓库。

验收入口：管理员「系统设置 → 邮件」。修改前显示余额耗尽预测面板；修改后移除该面板，金额阈值的余额不足提醒仍可见。旧字段提交兼容和保存行为另由 SettingsView Vitest 与后端设置 API 回归覆盖。

历史新增功能截图保留在 [原 CI 运行](https://github.com/ranxi2001/sub2api/actions/runs/41826772544)，仅说明旧版本界面。
