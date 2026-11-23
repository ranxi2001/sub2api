# 分组健康查询

开启 V3 渠道监控后，客户可以用自己的业务 API Key 查询主站最近五个已聚合分钟的分组表现。一个请求返回该 Key 已绑定、当前有权限使用的活跃分组。使用多把单分组 Key 时，分别用对应 Key 查询。

```sh
curl --fail-with-body "$BASE_URL/v1/sub2api/group-health" \
  -H "Authorization: Bearer $SUB2API_KEY"
```

也可使用 `x-api-key` 请求头。示例响应中的时间为 UTC。

```json
{
  "generated_at": "2026-11-23T10:07:20Z",
  "data_through": "2026-11-23T10:06:00Z",
  "window_start": "2026-11-23T10:01:00Z",
  "window_end": "2026-11-23T10:06:00Z",
  "fresh": true,
  "groups": [{
    "group_id": 1, "group_name": "Claude",
    "status": "degraded", "requests": 100, "errors": 8,
    "error_rate": 0.08, "ttft_p50_ms": 5000
  }]
}
```

`error_rate` 是分组最终失败数除以成功数与最终失败数之和，0.08 表示 8%。窗口左闭右开，按分组汇总各客户的真实流量，沿用监控对忽略错误类别的配置。重试过程中的上游错误和空闲探针不计入。延迟为首 Token 耗时中位数所在直方图区间的上界，单位为毫秒。没有延迟样本时返回 `null`。

可每分钟轮询。先确认 HTTP 200、`fresh=true`、`data_through` 已推进，再根据样本量、`error_rate` 和 `ttft_p50_ms` 应用自己的切换阈值。连续轮询可能覆盖相同请求。分组状态使用站点 V3 全局阈值。样本量低于站点下限时为 `insufficient`，无样本时为 `unknown` 且错误率为 `null`。聚合尚未启动、水位超前或落后超过五分钟时返回 `fresh=false`，各组状态均为 `unknown`。查询本身不会发起推理。

该接口提供站点侧分组流量的短窗口观测。合同约定的客户范围、结算周期、错误排除项和端到端延迟需要按双方约定另行统计。
