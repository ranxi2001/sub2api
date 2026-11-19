# 生图按 usage 计费与兼容记账

两种计费模式由模型价卡 `billing_mode` 决定，与响应有没有 `usage` 无关。

- `image`：按已完成图片数量与计费尺寸收费。响应中的上游 token 只作观测，不叠加收费。明确使用 image 价卡的 Responses 请求没有完成图片时，不按默认一张收费。
- `token`：按上游输入、缓存、图片输入和输出 token 计费。OpenAI 分组逐模型价卡可设置 `image_minimum_charge`，单位为**最终每张账面额度**，支持八位小数。实际收费是 `max(token 费用经过全部倍率后的金额, 完成图片张数 × 每张最低收费)`。已记录的 header-timeout 重试同时计入保底张数。不把最低收费再乘用户折扣或图片倍率。

分组 `model_pricing` 的 token 单价使用每 token 单位：每百万输出 $10 应填 `output_price=0.00001`、`image_output_price=0.00001`。最低收费 $0.01/张则填 `image_minimum_charge=0.01`，不除以一百万。管理页面展示每百万单价，并单独显示最终每张最低收费；模型广场同样保留两种单位。

最低收费默认未设置，不改变既有组。字段目前只支持 OpenAI 分组价卡，不支持渠道价卡。它是成功图片的收费下限，不是供应商实扣探测：供应商成本变化、失败但被供应商收费、未记录的重试仍需对账。相同组只绑定已核算成本的路线；混入更贵账号需要重新核算。原始 `usage_logs` token 与账号成本基价保持原样，最低收费及补差写入 `customer_cost_details`，钱包、Key 与订阅沿原有幂等结算链只扣一次。

## 供只接受 token 的客户记账

`POST /v1/images/generations` 与 `/v1/images/edits` 可通过请求头 `X-Image-Usage: billing` 选择**结算金额折算**模式。默认未发送此头时仍保留现有上游响应。此头不会改变计费模式、价卡或内部原始 token，也不会额外扣费。

兼容模式必须使用持久化结算，成功结算后才返回图片与折算 usage。响应包含：

```json
{
  "data": [{"url": "https://example.invalid/image"}],
  "upstream_usage": {"input_tokens": 25, "output_tokens": 1650},
  "usage": {"input_tokens": 0, "output_tokens": 2010000, "total_tokens": 2010000},
  "billing": {
    "usage_kind": "billing_equivalent",
    "billing_mode": "image",
    "actual_cost": 0.0201,
    "image_count": 1,
    "output_price_per_million": 0.01,
    "minimum_charge": 0,
    "minimum_charge_adjustment": 0
  }
}
```

客户端将**折算输出单价**配置为 $0.01/百万 token、输入单价为0；不要对这份折算 usage 再使用模型的真实 token 单价、组倍率或图片单价。每个折算 token 等于账本最小金额单位 1e-8，整数总量与八位小数的权威实际收费精确对应。原始上游 usage（若存在）保留在 `upstream_usage`，不得与折算 usage 重复累计。

JSON 和 SSE、生成和编辑、URL 与 b64、多图片均支持。请求 stream=true 时，既有上游 JSON 回退（包括误标 text/event-stream）仍按 JSON 返回；确认实际 SSE 协议后才发送注释心跳。SSE 的图片事件在结算后发送；整笔折算 usage 仅附在最后一个完成图片事件，partial 和 error 不承接整笔 usage，之前的 usage 移到 upstream_usage。部分成功按网关确认的完成图片结算，再返回已保留的成功/错误事件。临时响应文件权限0600，请求结束删除，单请求上限64MiB；这会增加完成图片交付延迟和临时磁盘占用。Responses/Chat Completions/Responses WebSocket 不支持此兼容头，会在发送上游前明确拒绝；其通常计费和原始 usage 不变。

回执要求本次请求新建的持久化事件已有权威结算证明；恢复 worker 先完成扣费和缓存确认时，前台仍可交付本次图片，不重复扣费或通知。legacy 去重记录、冲突和未知结算不生成成功回执。最低收费的 v3 明细保留已有 Free Fast／UltraFast Standard 快照、倍率及缓存价格，只附加最低收费和补差；不使用当前价卡重算历史日志。

结算失败、不明确的重复扣费或响应无法保留时不伪造已结算 usage。失败信息区分无法确认结算与已收费但响应不可交付，客户端不得自动重发生成任务，应使用原请求记录对账。该模式不承诺在数据库不可用、供应商异常扣费或客户端断开时实现无损交付。

## 上线与回滚

无数据库迁移，最低收费字段保存在既有分组 JSON 价卡内。旧二进制会忽略新字段：启用依赖保底的组之后，回滚前必须先停用这些组。仅配置字段不能使旧版本获得保底能力。PR 合并与线上安装分别验收；不得在旧版本公开启用宣称有最低收费的组。
