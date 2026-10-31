# 充值赠送抽奖

## 管理与使用

管理入口位于「智能运维 → 抽奖管理」，路径为 `/admin/lottery`，用户入口为 `/lottery`。

站点总开关 `lottery_enabled` 默认关闭，在抽奖管理页顶部开启。关闭时隐藏用户菜单、充值/兑换入口和通知提醒，停止新资格赠送与新抽奖；保留已有次数、中奖记录、请求幂等结果及退款核算。重新开启不会补发关闭期间的充值资格。管理员在关闭时仍可配置奖池。

新奖池默认关闭；先保存奖池并添加奖品，再分别启用赠送次数和抽奖。首次登录后台仍需由管理员确认项目原有的部署与运营承诺。

用户页以可切换奖池选项显示各池名称及可用次数。转盘显示奖品名称、金额或有效期；较窄扇区的奖品名称和概率显示在转盘下方，扇区面积仍按真实权重计算。奖品列表使用余额、订阅、文本三种图标。

顶部通知铃铛汇总未使用次数，点击各池提醒直接进入 `/lottery?pool=ID`。充值、兑换及抽奖成功后刷新；打开通知、重新聚焦页面及前台每 60 秒同步。公告的「全部已读」不消除未使用次数；退出登录或关闭抽奖会清空提醒。

奖池和奖品支持确认后删除，使用 `deleted_at` 保留历史引用。删除奖池停止赠送和抽奖，剩余次数无法使用；若需允许用户继续使用次数，应只关闭赠送开关。删除奖品后，剩余奖品概率重新计算。已经发放的权益、中奖快照和退款核算不受影响，旧编辑请求不能恢复已删除的配置。

每个奖池独立计算，一笔充值可以同时获得多个奖池的次数：

| 赠送方式 | 门槛为 100 USD 时 |
| --- | --- |
| 单笔达标 | 单笔到账 250 USD 送 1 次；两笔 50 USD 不送 |
| 累计达标 | 50 + 200 USD 送 2 次，保留 50 USD 进度 |

在线余额充值、余额兑换码、后台加款及“设置余额”的正向差额参与。金额以实际增加的站内 USD 余额为准，包含在线充值赠金。订阅购买、注册奖励、推广返利和抽奖奖金不参与。只有赠送开关开启时完成的新充值参与，无历史补发或次数过期。

产生充值参与流水后，门槛和赠送方式锁定。修改规则请创建新池；关闭旧池的赠送开关后，已有次数仍可使用，抽奖开关可单独控制。

## 奖品

- 余额：自动增加用户余额，不产生充值资格或推广返利。
- 订阅：选取上架套餐，保存套餐名称、分组和有效天数快照。同组有效订阅延长，过期订阅从当前时间重新开通。编辑同一套餐的奖品不会刷新原快照；更换套餐才会重新保存快照。
- 文本：中奖后显示保存的纯文本，可以填写“谢谢参与”。结果永久保存在抽奖记录，无人工兑奖流程。

权重范围为 1 至 1,000,000；库存 `-1` 表示无限，`0` 表示耗尽。每池最多 32 项奖品。仅启用、有库存且订阅分组有效的奖品参与概率计算。显示的概率为查询时的概率，最终按抽奖事务内的可用奖品计算。

库存编辑提交 `updated_at` 版本。表单打开后若奖品被修改或库存被消耗，保存会提示刷新，防止旧表单恢复已发出的库存。

## 事务、幂等与退款

金额转换为 1e-8 USD 的整数单位保存和计算，避免累计浮点误差。充值参与流水以 `(pool_id, source_type, source_id)` 唯一；抽奖以 `(user_id, request_key)` 唯一。

抽奖使用 `crypto/rand`。用户锁、按 ID 排序的奖池锁在同一事务内协调充值、退款和抽奖；奖品修改也先锁对应奖池。扣次数、减库存、保存结果、发放余额或订阅在同一事务提交，任一失败全部回滚。没有可用奖品时不扣次数。提交后刷新鉴权、余额及订阅缓存。

在线支付的内部兑换跳过赠送，在订单完成事务中登记资格；普通兑换和后台加款则与充值记录一起提交。抽奖请求必须携带 `Idempotency-Key`。同一用户重试同一键返回原结果；同一键不能改用于另一个奖池。用户页面会保存未确认的请求键，网络中断后可恢复结果。

退款按该订单累计退款 USD 金额，重新计算所有关联奖池的有效贡献。已使用的次数可以形成待抵扣数，后续充值先抵扣；已发奖品保留。重复的相同或更小退款累计金额不会再次扣减。资格流水记录每次充值、退款及抽奖的变化。

若网关退款成功但本地事务失败，订单保留扣减并进入现有 `REFUND_PENDING` 对账流程，避免再次提交网关退款。可在支付管理中查询和完成对账；自动查询能力取决于原有支付渠道是否支持退款查询。不要将这种订单作为新退款重新发起。

## API

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/lottery/pools` | 奖池、个人次数、累计进度、待抵扣数、公开概率 |
| POST | `/api/v1/lottery/pools/:id/draw` | 抽奖，必须带 `Idempotency-Key` |
| GET | `/api/v1/lottery/records` | 当前用户自己的分页记录 |
| GET / POST | `/api/v1/admin/lottery/pools` | 查询 / 创建奖池 |
| GET / PUT | `/api/v1/admin/lottery/settings` | 查询 / 修改站点总开关 |
| DELETE | `/api/v1/admin/lottery/pools/:id` | 删除奖池并保留历史 |
| DELETE | `/api/v1/admin/lottery/pools/:id/prizes/:prize_id` | 删除奖品并保留历史 |
| PUT | `/api/v1/admin/lottery/pools/:id` | 修改规则、名称和开关 |
| POST | `/api/v1/admin/lottery/pools/:id/prizes` | 添加奖品 |
| PUT | `/api/v1/admin/lottery/pools/:id/prizes/:prize_id` | 修改奖品和库存 |
| GET | `/api/v1/admin/lottery/records` | 管理员抽奖记录 |
| GET | `/api/v1/admin/lottery/ledger` | 管理员资格流水 |

后台写接口沿用幂等协调器，需要请求键。列表支持 `page`、`page_size`、`pool_id`；后台记录还支持 `user_id`。管理接口复用现有认证、审计、全局限流和后台权限校验，受限管理员密钥对应 `lottery.read` / `lottery.write` 权限。未中奖用户看不到文本奖的文本内容。

## 数据库与构建

增量迁移 `backend/migrations/323_recharge_lottery.sql` 新增六张 `lottery_*` 表；`324_lottery_controls.sql` 新增软删除字段并初始化默认关闭的总开关。升级已安装 323 的站点只追加 324。迁移由应用既有迁移器运行，不修改历史充值数据。Ent 模型及生成代码、Wire 生成代码均已生成并保存在源码目录。

充值和新抽奖按「用户 → 设置行共享锁 → 奖池」顺序加锁，总开关更新等待在途赠送/抽奖事务结束；退款始终保留处理能力，不受总开关和删除状态影响。奖池/奖品删除与配置修改均先锁奖池，与抽奖串行化。

工具链按仓库要求使用 Go 1.27.3、Node 22+、pnpm 9、PostgreSQL 16 和 Redis 7。前端先构建，后端用 `embed` 标签包含静态资源：

```sh
cd frontend
pnpm install --frozen-lockfile
pnpm run build
cd ../backend
go build -tags embed -o sub2api ./cmd/server
```

新增 PostgreSQL 集成测试仅允许使用名称含 `sub2api_lottery_test` 的独立数据库；测试会应用迁移并创建测试数据，不得指向生产数据库。

```sh
cd backend
LOTTERY_TEST_DSN='postgres://USER:PASSWORD@HOST:5432/sub2api_lottery_test?sslmode=disable' \
  go test -tags unit,integration ./internal/service -run TestLottery -count=1 -timeout=180s
```

前端覆盖转盘权重、无次数状态、请求重试、订阅刷新、奖池默认关闭、规则锁定及库存版本等行为。

```sh
cd frontend
pnpm exec vitest run src/components/lottery/wheel.spec.ts \
  src/views/user/__tests__/LotteryView.spec.ts \
  src/views/admin/__tests__/LotteryView.spec.ts \
  src/views/user/__tests__/RedeemView.spec.ts \
  src/views/user/__tests__/PaymentResultView.spec.ts \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm run typecheck
```
