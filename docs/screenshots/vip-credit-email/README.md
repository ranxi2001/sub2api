# VIP 自动垫付邮件通知截图

入口：管理员用户列表 → VIP 自动垫付配置。

来源为 [GitHub Actions](https://github.com/ranxi2001/sub2api/actions/runs/42976773223) 的实际 Vue 组件和 CSS 渲染，基线 `6fa7fb86eae7c732794450646dc13831dce87ff1`，候选 `cf4c106178b01607978c59920b89f2f798cfbec1`。数据和 HTTP 接口均为虚构 fixture；未连接生产或发送真实邮件。这是固定提交的历史证据，后续修复及基线整合的截图由对应 head 的远端 UI workflow 重新生成。

以下为已上传并回读校验字节数及 SHA256 的 GitHub 原生附件：

| 语言 / 设备 | 修改前 | 修改后，通知默认关闭 | 开启通知、保存并重新加载后 |
| --- | --- | --- | --- |
| 中文 / 桌面 | [截图](https://github.com/user-attachments/assets/ce7dc2dc-16e1-463b-8913-cbf3af7120f4) | [截图](https://github.com/user-attachments/assets/736ff541-cce8-480d-9a29-25b0729c33ca) | [截图](https://github.com/user-attachments/assets/851fdd25-d6c4-4d07-b4f1-85e4cf179e99) |
| 英文 / 移动端 | [截图](https://github.com/user-attachments/assets/8b8022ac-8ce0-4156-8689-6e879e0b3ad6) | [截图](https://github.com/user-attachments/assets/5fa63d64-0bc6-47ae-94d5-623df5a7f137) | [截图](https://github.com/user-attachments/assets/37c86618-b947-4d9a-9682-36a3af6806bc) |

[manifest.json](manifest.json) 保留原有四张中文截图的来源和哈希记录，所列文件名是远端产物名。本表六张附件及其余截图的完整 manifest（含 SHA256）和原始 PNG 位于同一 run 的 [artifact 13836680525](https://github.com/ranxi2001/sub2api/actions/runs/42976773223/artifacts/13836680525)。本目录仅保留文字说明和历史来源记录；四张曾提交的 PNG 已通过后续提交删除，历史提交仍保留。

这些截图展示配置弹窗，不作为邮件模板编辑器或完整页面导航验收。
