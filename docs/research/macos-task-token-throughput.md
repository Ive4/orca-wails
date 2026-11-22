# macOS 任务行的 token 输出指标

任务行显示 Codex 已报告的输出量和可见回答的估算速度。当前 Codex App Server 在单次模型响应完成后才有实际 token 用量，运行工具或等待用户输入还可能延迟通知。一次任务轮次可以包含多次模型响应，因此运行中的任务也可能分批拿到用量。精确的实时 `token/s` 需要生成期间的累计输出 token 采样与对应时间戳。 [Codex 响应流解析](https://github.com/openai/codex/blob/ccf9be8cc458bd6b8336fe84d68d8893f2691a5e/codex-rs/codex-api/src/sse/responses.rs#L476-L540) · [用量通知时机](https://github.com/openai/codex/blob/ccf9be8cc458bd6b8336fe84d68d8893f2691a5e/codex-rs/core/src/session/turn.rs#L3839-L3876)

## 实测

用 Codex CLI 0.171.0、独立的 `CODEX_HOME` 和本地合成 Responses SSE 验证了通知顺序。三条 `item/agentMessage/delta` 分别在约 0.17、0.47、0.77 秒到达。模拟服务发送 `response.completed` 后，约 1.07 秒收到 `thread/tokenUsage/updated`，其中 `outputTokens = 40`，`reasoningOutputTokens = 20`。约 1.08 秒收到 `turn/completed`。本次实验使用合成计数和延迟，验证的是通知顺序。 [Codex 0.171.0 发布](https://github.com/openai/codex/releases/tag/rust-v0.171.0) · [Orca 握手](https://github.com/stablyai/orca/blob/af41be458dc02c2710502586198d6d9acf83d389/src/main/codex/app-server/codex-app-server-handshake.ts)

第二次实验在 `thread/start` 显式开启内部选项 `experimentalRawEvents`，收到了 `rawResponse/completed`。它仍在响应完成后才给出该次确切用量，正常 Orca 握手的实验中没有收到这一通知。 [实验选项定义](https://github.com/openai/codex/blob/ccf9be8cc458bd6b8336fe84d68d8893f2691a5e/codex-rs/app-server-protocol/src/protocol/v2/thread.rs#L159-L168) · [内部用量通知](https://github.com/openai/codex/blob/ccf9be8cc458bd6b8336fe84d68d8893f2691a5e/codex-rs/app-server-protocol/src/protocol/v2/thread.rs#L2145-L2165)

`thread/tokenUsage/updated` 按 `threadId`、`turnId` 标识归属，提供线程累计计数 `total` 和最近一次用量报告 `last`。上下文重算和限额更新也可能触发同类通知，所以 `last` 不能逐条累加。`outputTokens` 已包含 `reasoningOutputTokens`。文本增量只提供字符串和所属消息标识。通知的字符数受语言影响，也可能成批到达。推理摘要还有在一个事件中发布整段文字的路径。 [Codex 计数映射](https://github.com/openai/codex/blob/ccf9be8cc458bd6b8336fe84d68d8893f2691a5e/codex-rs/codex-api/src/sse/responses.rs#L111-L148) · [上下文重算和限额更新](https://github.com/openai/codex/blob/ccf9be8cc458bd6b8336fe84d68d8893f2691a5e/codex-rs/core/src/session/mod.rs#L5855-L5907) · [Codex 文本和推理流](https://github.com/openai/codex/blob/ccf9be8cc458bd6b8336fe84d68d8893f2691a5e/codex-rs/core/src/session/turn.rs#L3681-L3823)

用同一版本的本机 Codex 回放了包含一次工具调用的两请求轮次。第一条 `thread/tokenUsage/updated` 发生于工具完成后，第二条发生于纯正文完成后。Orca 采集器累计两次真实上报的合成用量，跳过混有调用参数的第一条，只用第二条校准并结算正文速度。

## Cindy 和推理引擎的实现

Cindy 的 Codex 和 Pi 路径按英文词、单个符号和中日韩字符估算，Claude Code 路径使用字符换算。请求结算后，供应商上报的输出与推理用量用于校准后续样本。Cindy 区分近窗口内的流式估速与已结算请求的均速，按宿主收到数据的时间计量，排除工具执行及审批。压缩后的推理摘要只用于估算累计输出量。 [Cindy 字词估算](https://github.com/makecindy/cindy/blob/b7d676e2b2f79827eee20c98efc68c605a399066/packages/maker-core/src/agents/shared/calibrated-token-speed.ts#L112-L125) · [Cindy 计量口径](https://github.com/makecindy/cindy/blob/b7d676e2b2f79827eee20c98efc68c605a399066/docs/dev-rules/protocol-compatibility/live-generation-speed.md#L5-L24)

有推理引擎计量时可以直接使用引擎数据。Ollama CLI 把引擎报告的输出数除以生成耗时。llama.cpp 的 `timings_per_token` 可以在流中返回引擎统计。 [Ollama 计算](https://github.com/ollama/ollama/blob/2bd4d6aabdf4caa39e7095e201dcda4cc1a3a406/api/types.go#L1226-L1232) · [llama.cpp 流式计量](https://github.com/ggml-org/llama.cpp/blob/e300919cc704ddde3a8a8346a8714f57eeeee71b/tools/server/server-context.cpp#L2339-L2343)

## Orca 任务行

- **输出量使用真实计数。** 有同一主线程的本轮基线时，用 `total.outputTokens` 的差值显示 `本轮输出 1.2k tokens`。续跑、重连、计数回退及旧轮事件需要重新确认基线。
- **速度估算可见回答。** 使用 5 秒滚动窗口，按词和符号从正文估算 `≈ 10 token/s`，连续片段之间的同一个英文词只计一次。每次模型请求有完整正文且用量增量能配对时，用扣除推理后的上报量校准同一会话的后续估算。工具生成参数无法与正文精确拆分，会跳过这类请求的校准。长度至少 3 秒的可靠样本结算后可显示 `上次 10 token/s`，有效期 60 秒。结算均速排除首包、工具耗时及隐藏推理。工具结果、推理摘要和历史消息排除在实时速率之外。工具运行时优先显示工具，速率过期后恢复已报告用量或时间。
- **准确速率依赖生成源。** 如果后续 provider 提供同一模型响应内、带采样时间的生成 token 累计值，用相邻样本的输出增量除以采样间隔。

## 接入位置

在 Orca Core 的 Codex 实时通知入口记录主线程用量。目前同一个入口已接收 token 通知，但上下文占用路径只保留 `last.totalTokens` 和上下文窗口信息。用量采集按会话及当前轮次隔离，保存计数和采样时刻。`worktree.ps` 可以复用现有的结构化会话汇总关联，将可选字段传给 Go 的 `tasks.Row`，再由 SwiftUI 的 `taskMetrics` 展示。复用桌面现有的 3 秒轮询。元数据缺失时沿用现有显示，旧 Core、普通终端及其它 Agent 可继续使用同一界面。 [Orca Codex 通知入口](https://github.com/stablyai/orca/blob/af41be458dc02c2710502586198d6d9acf83d389/src/main/codex/structured/journal/codex-structured-journal-translation.ts#L236-L253) · [Orca 上下文占用](https://github.com/stablyai/orca/blob/af41be458dc02c2710502586198d6d9acf83d389/src/main/codex/structured/journal/codex-context-usage.ts) · [Core 结构化会话汇总关联](../../scripts/macos-task-timing.patch) · [桌面轮询](../../desktop.go#L610) · [任务行指标](../../native/macos/TrayPanel.swift#L466)

验收应覆盖同一轮多次模型响应、工具运行时的回退、已关闭任务、子 Agent、重放和重复事件、Core 重启及缺少字段的旧版本。可见速度还需要用中文、英文、代码与分批到达的文本核对显示。
