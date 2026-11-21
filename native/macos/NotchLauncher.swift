import AppKit
import SwiftUI

struct NotchGeometry: Equatable {
    static let wing: CGFloat = 28
    static let shoulder: CGFloat = 7
    let cutout: NSRect
    private let visibleFrame: NSRect

    @MainActor init?(screen: NSScreen) {
        self.init(display: screen.frame, visibleFrame: screen.visibleFrame,
                  safeTop: screen.safeAreaInsets.top, auxiliaryLeft: screen.auxiliaryTopLeftArea,
                  auxiliaryRight: screen.auxiliaryTopRightArea)
    }

    init?(display: NSRect, visibleFrame: NSRect, safeTop: CGFloat,
          auxiliaryLeft: NSRect?, auxiliaryRight: NSRect?) {
        guard safeTop > 0, let auxiliaryLeft, !auxiliaryLeft.isEmpty,
              let auxiliaryRight, !auxiliaryRight.isEmpty else { return nil }
        let left = auxiliaryLeft.maxX
        let right = auxiliaryRight.minX
        guard right - left >= 48, left - Self.wing >= visibleFrame.minX,
              right + Self.wing <= visibleFrame.maxX else { return nil }
        cutout = NSRect(x: left, y: display.maxY - safeTop, width: right - left, height: safeTop)
        self.visibleFrame = visibleFrame
    }

    var headerHeight: CGFloat { cutout.height }
    var panelWidth: CGFloat { min(386, visibleFrame.width - 16 - 2 * Self.shoulder) }

    func compact(hasCount: Bool) -> NSRect {
        NSRect(x: cutout.minX - Self.wing, y: cutout.minY,
               width: cutout.width + Self.wing * (hasCount ? 2 : 1), height: cutout.height)
    }

    func expanded(contentHeight: CGFloat) -> NSRect {
        let width = panelWidth + 2 * Self.shoulder
        let x = min(max(cutout.midX - width / 2, visibleFrame.minX + 8), visibleFrame.maxX - 8 - width)
        let height = min(contentHeight + headerHeight, cutout.maxY - visibleFrame.minY - 8)
        return NSRect(x: x, y: cutout.maxY - height, width: width, height: height)
    }
}

@MainActor
final class NotchLauncher: NSPanel {
    let geometry: NotchGeometry
    let button: NSButton = NotchButton()
    let countButton: NSButton = NotchButton()
    private let surface = NotchSurface()
    private var host: TrayHostingView?
    private var contentHeight: CGFloat = 0
    private var hasCount = false
    private var lastLights: TrayLights?
    private var animation: Timer?
    private var destination: NSRect?
    var onToggle: (() -> Void)?
    var onCount: (() -> Void)?
    var onHover: ((Bool) -> Void)? { didSet { surface.onHover = onHover } }
    var onClose: (() -> Void)?

    override var canBecomeKey: Bool { host != nil }

    init(geometry: NotchGeometry) {
        self.geometry = geometry
        super.init(contentRect: geometry.compact(hasCount: false),
                   styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        identifier = NSUserInterfaceItemIdentifier("orca-notch-launcher")
        level = .statusBar
        collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
        appearance = NSAppearance(named: .darkAqua)
        isOpaque = false
        backgroundColor = .clear
        hasShadow = false
        hidesOnDeactivate = false
        isReleasedWhenClosed = false
        surface.cutoutHeight = geometry.cutout.height
        surface.wantsLayer = true
        surface.layer?.masksToBounds = true
        contentView = surface

        for (control, action) in [(button, #selector(toggle)), (countButton, #selector(selectCount))] {
            control.isBordered = false
            control.target = self
            control.action = action
            surface.addSubview(control)
        }
        button.setAccessibilityLabel("Orca 刘海面板")
        countButton.isHidden = true
        setShape(geometry.compact(hasCount: false))
    }

    override func constrainFrameRect(_ frameRect: NSRect, to screen: NSScreen?) -> NSRect { frameRect }

    func expand(_ panel: TrayPanel, key: Bool) {
        host?.removeFromSuperview()
        let host = TrayHostingView(rootView: panel)
        var content = panel
        content.onHeightChange = { [weak self, weak host] height in
            guard let self, self.host === host, height > 0 else { return }
            self.contentHeight = height
            self.resize()
        }
        host.rootView = content
        self.host = host
        surface.addSubview(host, positioned: .below, relativeTo: button)
        contentHeight = host.fittingSize.height
        hasShadow = true
        resize()
        if key { pin() }
        else { orderFrontRegardless() }
    }

    func pin() { makeKeyAndOrderFront(nil) }

    func finishAnimation() { resize(animated: false) }

    func collapse(animated: Bool = true) {
        if isKeyWindow { resignKey() }
        host?.removeFromSuperview()
        host = nil
        contentHeight = 0
        hasShadow = false
        resize(animated: animated)
    }

    func hitControl(_ point: NSPoint) -> Bool {
        let hit = surface.convert(point, from: nil)
        return button.frame.contains(hit) || (!countButton.isHidden && countButton.frame.contains(hit))
    }

    func updateLabel(lights: TrayLights, summary: String) {
        let description = "Orca 刘海面板 · " + summary
        guard button.toolTip != description || lastLights != lights else { return }
        lastLights = lights
        button.toolTip = description
        button.setAccessibilityLabel(description)
        let attention = lights.waiting + lights.blocked
        let count = attention > 0 ? attention : lights.running
        let color = attention == 0 ? NSColor(srgbRed: 0.60, green: 0.83, blue: 1, alpha: 1) :
            NSColor(srgbRed: 1, green: 0.82, blue: 0.56, alpha: 1)
        countButton.attributedTitle = NSAttributedString(string: count > 99 ? "99+" : "\(count)", attributes: [
            .font: NSFont.monospacedDigitSystemFont(ofSize: 11, weight: .semibold),
            .foregroundColor: color,
        ])
        countButton.setAccessibilityLabel(attention > 0 ? "\(count) 项待处理" : "\(count) 项运行或监测")
        countButton.toolTip = summary
        let symbol = lights.waiting > 0 ? "pause.fill" : lights.blocked > 0 ? "exclamationmark.octagon" : "circle.fill"
        let configuration = NSImage.SymbolConfiguration(pointSize: 10, weight: .semibold)
            .applying(.init(paletteColors: [color]))
        (countButton as? NotchButton)?.statusGlyph = NSImage(systemSymbolName: symbol, accessibilityDescription: nil)?
            .withSymbolConfiguration(configuration)
        hasCount = count > 0
        countButton.isHidden = !hasCount
        resize()
    }

    private func resize(animated: Bool = true) {
        let target = host == nil ? geometry.compact(hasCount: hasCount) : geometry.expanded(contentHeight: contentHeight)
        guard destination != target || !animated else { return }
        destination = target
        animation?.invalidate()
        animation = nil
        let start = frame
        guard isVisible, animated, start != target, !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion else {
            setShape(target)
            return
        }
        let began = ProcessInfo.processInfo.systemUptime
        let duration = target.height > start.height ? 0.24 : 0.185
        let timer = Timer(timeInterval: 1.0 / 60, repeats: true) { [weak self] timer in
            guard let self else { timer.invalidate(); return }
            let finished = MainActor.assumeIsolated {
                let t = min(1, (ProcessInfo.processInfo.systemUptime - began) / duration)
                let amount = 1 - pow(1 - t, 4)
                let x = start.minX + (target.minX - start.minX) * amount
                let width = start.width + (target.width - start.width) * amount
                let height = start.height + (target.height - start.height) * amount
                self.setShape(t == 1 ? target : NSRect(x: x, y: target.maxY - height, width: width, height: height))
                if t == 1 {
                    self.animation = nil
                    self.invalidateShadow()
                }
                return t == 1
            }
            if finished { timer.invalidate() }
        }
        RunLoop.main.add(timer, forMode: .common)
        animation = timer
    }

    private func setShape(_ next: NSRect) {
        if frame != next { setFrame(next, display: false, animate: false) }
        let shoulder = surface.shoulderInset
        let leftClear = geometry.cutout.minX - frame.minX - shoulder
        let rightClear = frame.maxX - geometry.cutout.maxX - shoulder
        let expansion = min(1, max(0, (frame.height - geometry.cutout.height) / 28))
        let catWidth = 26 + 5 * expansion
        let countWidth = 26 + 27 * expansion
        let controlHeight = geometry.cutout.height - 4 + expansion
        button.frame = NSRect(x: shoulder + max(1, (leftClear - catWidth) / 2),
                              y: 2, width: catWidth, height: controlHeight)
        countButton.frame = NSRect(x: frame.width - shoulder - countWidth - max(1, (rightClear - countWidth) / 2),
                                   y: 2, width: countWidth, height: controlHeight)
        (button as? NotchButton)?.imageSide = 17 + 3 * expansion
        (countButton as? NotchButton)?.statusVisibility = expansion
        host?.frame = NSRect(x: (frame.width - geometry.panelWidth) / 2, y: geometry.headerHeight,
                             width: geometry.panelWidth, height: contentHeight)
        surface.needsDisplay = true
        displayIfNeeded()
    }

    @objc private func toggle() { onToggle?() }
    @objc private func selectCount() { onCount?() }
    override func cancelOperation(_ sender: Any?) { onClose?() }
}

private final class NotchSurface: NSView {
    var cutoutHeight: CGFloat = 0
    var onHover: ((Bool) -> Void)?
    private var tracking: NSTrackingArea?
    override var isFlipped: Bool { true }
    var shoulderInset: CGFloat { min(NotchGeometry.shoulder, max(0, bounds.height - cutoutHeight) / 4) }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if tracking == nil {
            let area = NSTrackingArea(rect: .zero, options: [.activeAlways, .mouseEnteredAndExited, .inVisibleRect],
                                      owner: self, userInfo: nil)
            addTrackingArea(area)
            tracking = area
        }
    }

    override func mouseEntered(with event: NSEvent) { onHover?(true) }
    override func mouseExited(with event: NSEvent) { onHover?(false) }

    override func draw(_ dirtyRect: NSRect) {
        guard let context = NSGraphicsContext.current?.cgContext else { return }
        let w = bounds.width, h = bounds.height
        let depth = max(0, h - cutoutHeight)
        let corner = min(24, 11 + depth / 6)
        let shoulder = shoulderInset
        let path = CGMutablePath()
        path.move(to: .zero)
        path.addQuadCurve(to: CGPoint(x: shoulder, y: shoulder), control: CGPoint(x: shoulder, y: 0))
        path.addLine(to: CGPoint(x: shoulder, y: h - corner))
        path.addQuadCurve(to: CGPoint(x: shoulder + corner, y: h), control: CGPoint(x: shoulder, y: h))
        path.addLine(to: CGPoint(x: w - shoulder - corner, y: h))
        path.addQuadCurve(to: CGPoint(x: w - shoulder, y: h - corner), control: CGPoint(x: w - shoulder, y: h))
        path.addLine(to: CGPoint(x: w - shoulder, y: shoulder))
        path.addQuadCurve(to: CGPoint(x: w, y: 0), control: CGPoint(x: w - shoulder, y: 0))
        path.closeSubpath()
        context.addPath(path)
        if depth > 0, let gradient = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB),
                colors: [NSColor.black.cgColor, NSColor(srgbRed: 13.0 / 255, green: 18.0 / 255, blue: 29.0 / 255, alpha: 1).cgColor] as CFArray,
                locations: [0, 1]) {
            context.clip()
            context.drawLinearGradient(gradient, start: CGPoint(x: 0, y: cutoutHeight),
                                       end: CGPoint(x: 0, y: h), options: [.drawsBeforeStartLocation])
        } else {
            context.setFillColor(NSColor.black.cgColor)
            context.fillPath()
        }
    }
}

private final class NotchButton: NSButton {
    var imageSide: CGFloat = 17
    var statusGlyph: NSImage?
    var statusVisibility: CGFloat = 0
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func resetCursorRects() { addCursorRect(bounds, cursor: .pointingHand) }

    override func draw(_ dirtyRect: NSRect) {
        if isHighlighted {
            NSColor.white.withAlphaComponent(0.16).setFill()
            NSBezierPath(roundedRect: bounds.insetBy(dx: 3, dy: 3), xRadius: 7, yRadius: 7).fill()
        }

        if let image {
            let side = imageSide
            let rect = CGRect(x: bounds.midX - side / 2, y: bounds.midY - side / 2, width: side, height: side)
            image.draw(in: rect, from: .zero, operation: .sourceOver, fraction: 1, respectFlipped: true, hints: nil)
        } else {
            let label = attributedTitle
            let size = label.size()
            let glyphOffset = 16 * statusVisibility
            let x = bounds.midX - (size.width + glyphOffset) / 2
            if let statusGlyph, statusVisibility > 0 {
                statusGlyph.draw(in: NSRect(x: x, y: bounds.midY - 5.5, width: 11, height: 11),
                                 from: .zero, operation: .sourceOver, fraction: statusVisibility,
                                 respectFlipped: true, hints: nil)
            }
            label.draw(at: NSPoint(x: x + glyphOffset, y: bounds.midY - size.height / 2))
        }
    }
}
