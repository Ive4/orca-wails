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
                  startedAt: nil, updatedAt: nil, quickReply: true),
            .init(id: "finished", title: "Finished", workspace: "", status: "done",
                  startedAt: now - 1_000, updatedAt: nil, quickReply: nil),
            .init(id: "old", title: "Old", workspace: "", status: "done",
                  startedAt: now - 90_000_000, updatedAt: nil, quickReply: nil),
        ], partial: false)
        precondition(taskSnapshot.taskRows(.all).map(\.id) == ["live", "finished"])
        precondition(taskSnapshot.taskRows(.current).map(\.id) == ["live"])
        precondition(taskSnapshot.taskRows(.recent).map(\.id) == ["finished"])
        var lights = TrayLights(TraySnapshot())
        var ring = TrayRing()
        lights.waiting = 1
        ring.update(lights, at: 1, reduceMotion: false)
        let single = ring.frame(at: 2, reduceMotion: false)
        precondition(single.sectors.count == 1 && single.sectors[0].fraction == 1)
        precondition(single.rotation == 0 && single.breath < 1)

        lights.running = 1
        ring.update(lights, at: 2, reduceMotion: false)
        let multiple = ring.frame(at: 3, reduceMotion: false)
        precondition(multiple.sectors.count == 2 && multiple.sectors.allSatisfy { $0.fraction == 0.5 })
        precondition(multiple.rotation != 0 && multiple.breath == 1)

        ring.update(TrayLights(TraySnapshot()), at: 3, reduceMotion: true)
        ring.confirmReply(at: 4)
        precondition(ring.frame(at: 4.1, reduceMotion: false).replyGlow > 0)
        precondition(ring.frame(at: 4.1, reduceMotion: true).replyGlow == 0)
        precondition(ring.frame(at: 4.4, reduceMotion: false).replyGlow == 0)
        print("macOS tray anchor and motion checks passed")
    }
}
