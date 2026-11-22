# 个人微信任务助手实施计划

**目标** 在 Orca-Wails 连接个人微信，查询本机任务，从微信回复原会话，并推送阻塞和完成结果。

**规格** [个人微信任务助手设计](../specs/2026-11-22-personal-wechat-task-assistant-design.md)

**结构** Go 直连腾讯 iLink，复用现有 Core 的任务快照和结构化交互。SwiftUI 在现有「通知」设置下呈现连接状态和操作。原型继续用于演示任务流。

- [x] iLink 客户端（`internal/wechat/client.go`）。用 HTTP 测试服务器验证扫码、PIN、认证请求、长轮询和纯文字收发，以及错误响应和不受信任的认证目标。
- [x] 微信任务操作（`internal/wechat/tasks.go`）。验证回复码与问题版本绑定、分组和多选、过期回复和完成摘要。复用现有 Core 任务与结构化回复接口。
- [x] 后台连接（`internal/wechat/service.go`、`wechat.go`）。隔离收发凭据和设置快照，验证扫码身份、旧绑定回复失效、完成通知去重，以及重连后保留未发送的完成结果。
- [x] 原生界面（`native/macos/TrayWeChat.swift`、`TrayPanel.swift`）。覆盖未连接、扫码、等待 PIN、私聊配对、已连接、重连和断开状态。
- [x] 完成 Go 与 Swift 构建、受控服务端验证、桌面和窄屏原型渲染检查。验证打包 Core 拒绝普通 CLI 和错误凭据访问团队问题。
- [ ] 使用真实个人微信，验证实际团队 Bot 的阻塞回复、完成推送和隔夜主动送达。
