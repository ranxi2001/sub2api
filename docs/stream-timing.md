# 流式响应分段计时

使用记录 → 延迟 → 请求耗时详情 → HTTP 尝试中查看新增字段。数据沿用
request_timing_details JSON，不改变 first_token_ms 或计费，不需要数据库迁移。
旧记录及未覆盖路径显示未采集。

首完整 SSE 写出、首语义、推理和正文的独立指标，以及原生 WS 和可信插件的采集边界，见 [OpenAI 流式首事件与内容计时](openai-sse-delivery.md)。下文的物理 HTTP 正文阶段仍保留其原有含义，不将 WS 握手或 gRPC 响应头映射为 HTTP 正文字节。

- raw_body_first：HTTP 响应体在手动解压之前，第一次 Read 返回非空数据。
  这是应用读取边界，不是网卡到包时间；不包含 HTTP 响应头。
- decoded_body_first：手动解压之后第一次非空读取。无压缩时同样记录。
- sse_parsed_first：OpenAI Responses 首个可解析的完整 SSE data JSON。
  是数据行解析边界，不保证已经消费空行分隔符。
- sse_visible_first：上述解析边界首次出现非空可见输出。
- tool_converted_first / tool_visible_first：客户端工具转换器产出首个事件 /
  首个可见事件的时间；并非客户端接收时间。未走转换器时保持缺失。

以上时间均从完整请求开始计算，按物理 HTTP 尝试隔离；阶段观察向父级
egress 汇总。首个事件可能只是元数据，检查工具缓冲时应比较首个可见事件。

Content-Encoding 在手动解压清除响应头之前记录。identity 表示未声明编码；
auto_decompressed=true 表示 Go HTTP 客户端已自动解 gzip，原始字节不可见，
因此 raw_body_first 不填，不能把解压后数据冒充解压前数据。
若编码不受支持或解压初始化失败、编码响应头仍保留，decoded_body_first
也保持缺失，避免将仍然编码的数据误认为已解码。

解压、解析和转换的首数据间隔包含后续数据到达、调度和背压等待，不是
纯 CPU 耗时。较大的间隔用于定位下一步检查边界，不能单凭间隔证明组件故障。
若原始正文首数据也很晚，应继续检查上游和代理；若原始正文早但解压后晚，
检查压缩帧 Flush 与解压；若解压后早但 SSE JSON 晚，检查事件分帧；
若转换前可见事件早而转换后晚，检查工具参数缓冲。

采集范围：通用 HTTP 出站的正文边界与编码，以及 OpenAI Responses 原生、
透传解析和原生客户端工具转换路径。WebSocket 和其他协议不伪造同名阶段。
