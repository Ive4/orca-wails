import AppKit

@main
struct TrayChecks {
    @MainActor
    static func main() {
        let screen = NSRect(x: -1200, y: 130, width: 1200, height: 720)
        let anchor = NSRect(x: -620, y: 850, width: 30, height: 24)
        let size = NSSize(width: 336, height: 285)
        let normal = TrayWindow.panelFrame(anchoredTo: anchor, visibleFrame: screen, size: size)
        precondition(normal.midX == anchor.midX && normal.maxY == anchor.minY - 4)
        let resized = TrayWindow.panelFrame(anchoredTo: anchor, visibleFrame: screen,
                                             size: NSSize(width: 336, height: 140))
        precondition(resized.midX == normal.midX && resized.maxY == normal.maxY)

        for x in [screen.minX, screen.maxX - anchor.width] {
            let atEdge = TrayWindow.panelFrame(anchoredTo: NSRect(x: x, y: anchor.minY, width: anchor.width,
                                                                 height: anchor.height),
                                               visibleFrame: screen, size: size)
            precondition(atEdge.minX >= screen.minX + 8 && atEdge.maxX <= screen.maxX - 8)
        }
        let smallScreen = NSRect(x: 0, y: 0, width: 280, height: 260)
        let limited = TrayWindow.panelFrame(anchoredTo: NSRect(x: 262, y: 260, width: 18, height: 24),
                                            visibleFrame: smallScreen, size: size)
        precondition(smallScreen.contains(limited) && limited.minY == smallScreen.minY + 8)
        let display = NSRect(x: -1728, y: 150, width: 1728, height: 1117)
        let usable = NSRect(x: -1679, y: 150, width: 1679, height: 1084)
        let left = NSRect(x: -1728, y: 1235, width: 771.5, height: 32)
        let right = NSRect(x: -771.5, y: 1235, width: 771.5, height: 32)
        let notch = NotchGeometry(display: display, visibleFrame: usable,
                                  safeTop: 32, auxiliaryLeft: left, auxiliaryRight: right)
        precondition(notch != nil)
        if let notch {
            precondition(notch.compact(hasCount: false) == NSRect(x: -984.5, y: 1235, width: 213, height: 32))
            precondition(notch.compact(hasCount: true) == NSRect(x: -984.5, y: 1235, width: 241, height: 32))
            let expanded = notch.expanded(contentHeight: 285)
            precondition(notch.headerHeight == 32 && notch.panelWidth == 386)
            precondition(expanded == NSRect(x: -1064, y: 950, width: 400, height: 317))
        }
        precondition(NotchGeometry(display: display, visibleFrame: usable,
                                   safeTop: 0, auxiliaryLeft: nil, auxiliaryRight: nil) == nil)
        precondition(NotchGeometry(display: display, visibleFrame: usable,
                                   safeTop: 32, auxiliaryLeft: nil, auxiliaryRight: right) == nil)

        let now = UInt64(Date().timeIntervalSince1970 * 1000)
        var taskSnapshot = TraySnapshot()
        taskSnapshot.summary = .init(rows: [
            .init(id: "live", title: "Live", workspace: "", status: "waiting",
                  startedAt: nil, updatedAt: nil, completedDurationSeconds: nil, quickReply: true),
            .init(id: "finished", title: "Finished", workspace: "", status: "done",
                  startedAt: now - 1_000, updatedAt: nil, completedDurationSeconds: nil, quickReply: nil),
            .init(id: "old", title: "Old", workspace: "", status: "done",
                  startedAt: now - 90_000_000, updatedAt: nil, completedDurationSeconds: nil, quickReply: nil),
        ], partial: false)
        precondition(taskSnapshot.taskRows(.all).map(\.id) == ["live", "finished"])
        precondition(taskSnapshot.taskRows(.current).map(\.id) == ["live"])
        precondition(taskSnapshot.taskRows(.recent).map(\.id) == ["finished"])
        let completed = try! JSONDecoder().decode(TrayTask.self, from: Data("""
            {"id":"done","title":"完成任务","workspace":"Orca","status":"done",
             "startedAt":1700058766000,"updatedAt":1700059500000,"completedDurationSeconds":522}
            """.utf8))
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        let completion = completed.completionTiming(now: Date(timeIntervalSince1970: 1700059200), calendar: calendar)
        precondition(completion.finishedAt == "14:32 结束" && completion.duration == "耗时 8分42秒")
        let nextDay = completed.completionTiming(now: Date(timeIntervalSince1970: 1700092800), calendar: calendar)
        precondition(nextDay.finishedAt == "昨天 14:32 结束")
        let unfinishedTime = try! JSONDecoder().decode(TrayTask.self, from: Data("""
            {"id":"done","title":"完成任务","workspace":"Orca","status":"done","updatedAt":1700058766000}
            """.utf8))
        let missing = unfinishedTime.completionTiming(now: Date(timeIntervalSince1970: 1700059200), calendar: calendar)
        precondition(missing.finishedAt == nil && missing.duration == nil)

        let active = try! JSONDecoder().decode(TrayTask.self, from: Data("""
            {"id":"live","title":"运行任务","workspace":"Orca","status":"working","currentTool":"Read",
             "startedAt":1700058766000,"updatedAt":1700058900000}
            """.utf8))
        let timing = active.activeTiming(now: Date(timeIntervalSince1970: 1700059200))
        precondition(active.currentTool == "Read" && timing.elapsedSince == Date(timeIntervalSince1970: 1700058766)
                     && timing.updatedAt == Date(timeIntervalSince1970: 1700058900))
        let firstUpdate = active.activeTiming(now: Date(timeIntervalSince1970: 1700058766))
        precondition(firstUpdate.elapsedSince == timing.elapsedSince && firstUpdate.updatedAt == nil)
        let undated = unfinishedTime.activeTiming(now: Date(timeIntervalSince1970: 1700059200))
        precondition(undated.elapsedSince == nil && undated.updatedAt == timing.elapsedSince)

        checkTaskOutputMetrics()
        checkTaskStatusDescriptions()
        var lights = TrayLights(TraySnapshot())
        var ring = TrayRing()
        lights.waiting = 1
        ring.update(lights, at: 1, reduceMotion: false)
        let single = ring.frame(at: 2, reduceMotion: false)
        precondition(single.sectors.count == 1 && single.sectors[0].fraction == 1)
        precondition(single.rotation == 0 && single.breath < 1)

        lights.working = 1
        ring.update(lights, at: 2, reduceMotion: false)
        let multiple = ring.frame(at: 3, reduceMotion: false)
        precondition(multiple.sectors.count == 2 && multiple.sectors.allSatisfy { $0.fraction == 0.5 })
        precondition(multiple.rotation != 0 && multiple.breath == 1)

        ring.update(TrayLights(TraySnapshot()), at: 3, reduceMotion: true)
        ring.confirmReply(at: 4)
        precondition(ring.frame(at: 4.1, reduceMotion: false).replyGlow > 0)
        precondition(ring.frame(at: 4.1, reduceMotion: true).replyGlow == 0)
        precondition(ring.frame(at: 4.4, reduceMotion: false).replyGlow == 0)
        checkRingContrast()
        precondition(trayDiffLines(before: "keep\nold\nend", after: "keep\nnew\nend") ==
                     ["  keep", "− old", "+ new", "  end"])
        precondition(trayDiffLines(before: "", after: "added") == ["+ added"])
        precondition(trayDiffLines(before: "removed", after: "") == ["− removed"])
        print("macOS tray anchor, task metrics, motion, contrast and diff checks passed")
    }

    private static func checkTaskOutputMetrics() {
        let now = Date(timeIntervalSince1970: 1700059200)
        let expires = now.addingTimeInterval(2)
        let metrics = #"{"turnOutputTokens":1200,"visibleTokensPerSecond":18,"visibleUntil":1700059202000}"#
        func task(_ status: String = "working", tool: String? = nil, metrics: String? = nil) -> TrayTask {
            let optional = [tool.map { #", "currentTool":"\#($0)""# },
                            metrics.map { #", "outputMetrics":\#($0)"# }].compactMap { $0 }.joined()
            return try! JSONDecoder().decode(TrayTask.self, from: Data("""
                {"id":"task","title":"任务","workspace":"Orca","status":"\(status)",
                 "startedAt":1700058766000,"updatedAt":1700058900000\(optional)}
                """.utf8))
        }

        let active = task(tool: "Read", metrics: metrics)
        precondition(active.visibleOutputExpiry == expires)
        precondition(active.activeMetricLabel(now: now) == "≈ 18 token/s")
        precondition(active.activeMetricLabel(now: expires) == "本轮输出 1.2k tokens")
        let settled = #"{"turnOutputTokens":1200,"visibleTokensPerSecond":18,"visibleUntil":1700059202000,"requestRate":true}"#
        precondition(task(metrics: settled).activeMetricLabel(now: now) == "上次 18 token/s")
        precondition(task(metrics: settled).activeMetricLabel(now: expires) == "本轮输出 1.2k tokens")
        let settledWithTool = #"{"turnOutputTokens":1200,"visibleTokensPerSecond":18,"visibleUntil":1700059202000,"requestRate":true,"toolActive":true}"#
        precondition(task(tool: "Read", metrics: settledWithTool).activeMetricLabel(now: now) == "Read")
        let runningToolMetrics = #"{"turnOutputTokens":1200,"visibleTokensPerSecond":18,"visibleUntil":1700059202000,"toolActive":true}"#
        let runningTool = task(tool: "Read", metrics: runningToolMetrics)
        precondition(runningTool.activeMetricLabel(now: now) == "≈ 18 token/s")
        precondition(runningTool.activeMetricLabel(now: expires) == "Read")
        precondition(task(metrics: runningToolMetrics).activeMetricLabel(now: expires) == "本轮输出 1.2k tokens")
        precondition(task(tool: "Read", metrics: #"{"turnOutputTokens":12,"toolActive":"true"}"#)
            .activeMetricLabel(now: now) == "本轮输出 12 tokens")
        precondition(task(metrics: #"{"turnOutputTokens":999,"visibleTokensPerSecond":18}"#)
            .activeMetricLabel(now: now) == "本轮输出 999 tokens")
        precondition(task(metrics: #"{"turnOutputTokens":12,"visibleTokensPerSecond":"bad","visibleUntil":1700059202000}"#)
            .activeMetricLabel(now: now) == "本轮输出 12 tokens")
        precondition(task(metrics: #"{"turnOutputTokens":"bad","visibleTokensPerSecond":18,"visibleUntil":1700059202000}"#)
            .activeMetricLabel(now: now) == "≈ 18 token/s")
        precondition(task(metrics: #""malformed""#).activeMetricLabel(now: now) == nil)
        precondition(task(tool: "Read").activeMetricLabel(now: now) == "Read")
        precondition(task().activeMetricLabel(now: now) == nil)
        precondition(task().activeTiming(now: now).updatedAt == Date(timeIntervalSince1970: 1700058900))
        precondition(task("monitoring", tool: "Watch", metrics: metrics).activeMetricLabel(now: now) == "Watch")
        for status in ["monitoring", "waiting", "blocked", "done", "unknown"] {
            let inactive = task(status, metrics: metrics)
            precondition(inactive.visibleOutputExpiry == nil && inactive.activeMetricLabel(now: now) == nil)
        }
    }

    private static func checkTaskStatusDescriptions() {
        var snapshot = TraySnapshot()
        snapshot.taskState = "ready"
        precondition(TrayLights(snapshot).statusDescription(for: snapshot) == "当前无活动任务")
        snapshot.summary = .init(rows: ["working", "monitoring", "waiting", "blocked"].map { status in
            TrayTask(id: status, title: "", workspace: "", status: status,
                     startedAt: nil, updatedAt: nil, completedDurationSeconds: nil, quickReply: nil)
        }, partial: true)
        let active = TrayLights(snapshot)
        precondition(active.running == 2 && !active.unconfirmed)
        precondition(active.statusDescription(for: snapshot) == "1 项运行中 · 1 项监测中 · 1 项等待输入 · 1 项受阻 · 部分工作区")
        snapshot.summary = .init(rows: [], partial: true)
        precondition(TrayLights(snapshot).statusDescription(for: snapshot) == "任务状态待确认 · 部分工作区")
        snapshot.taskState = "loading"
        precondition(TrayLights(snapshot).unconfirmed)
        precondition(TrayLights(snapshot).statusDescription(for: snapshot) == "正在读取任务…")
        snapshot.taskState = "unavailable"
        precondition(TrayLights(snapshot).unconfirmed)
        precondition(TrayLights(snapshot).statusDescription(for: snapshot) == "暂时读不到任务状态")
    }

    @MainActor
    private static func checkRingContrast() {
        _ = NSApplication.shared
        for dark in [false, true] {
            let background = dark ? 0.15 : 0.92
            for status in ["working", "waiting", "blocked"] {
                let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 44, pixelsHigh: 44,
                                              bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                              colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
                NSGraphicsContext.saveGraphicsState()
                NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
                NSGraphicsContext.current?.cgContext.scaleBy(x: 2, y: 2)
                let bounds = NSRect(x: 0, y: 0, width: 22, height: 22)
                NSColor(srgbRed: background, green: background, blue: background, alpha: 1).setFill()
                bounds.fill()
                var snapshot = TraySnapshot()
                snapshot.taskState = "ready"
                snapshot.summary = .init(rows: [TrayTask(id: status, title: "", workspace: "", status: status,
                                                        startedAt: nil, updatedAt: nil,
                                                        completedDurationSeconds: nil, quickReply: nil)], partial: false)
                var ring = TrayRing()
                ring.update(TrayLights(snapshot), at: 0, reduceMotion: false)
                TrayIcon.image(nil, frame: .init(dark: dark, ring: ring.frame(at: 1.8, reduceMotion: false))).draw(in: bounds)
                NSGraphicsContext.restoreGraphicsState()

                let backdrop = luminance(bitmap.colorAt(x: 0, y: 0)!)
                let legible = (0..<72).filter { index in
                    let angle = Double(index) * .pi / 36
                    let x = Int(22 + cos(angle) * 18.4)
                    let y = Int(22 + sin(angle) * 18.4)
                    let ring = luminance(bitmap.colorAt(x: x, y: y)!)
                    return (max(ring, backdrop) + 0.05) / (min(ring, backdrop) + 0.05) >= 3
                }
                precondition(legible.count >= 66, "The task ring must stay distinct at the bottom of its pulse")
            }
        }
    }

    private static func luminance(_ color: NSColor) -> Double {
        let rgb = color.usingColorSpace(.sRGB)!
        func linear(_ value: Double) -> Double {
            value <= 0.04045 ? value / 12.92 : pow((value + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * linear(rgb.redComponent) + 0.7152 * linear(rgb.greenComponent) + 0.0722 * linear(rgb.blueComponent)
    }
}
