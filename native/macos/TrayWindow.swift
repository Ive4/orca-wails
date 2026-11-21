import AppKit
import SwiftUI

@MainActor
final class TrayWindow: NSPanel {
    enum Placement { case menuBar, notch }

    private let anchor: NSRect
    private let visibleFrame: NSRect
    private let material = NSVisualEffectView()
    private static let tipHeight: CGFloat = 6
    static let cornerRadius: CGFloat = 15
    private static let maskHeight = tipHeight + 2 * cornerRadius + 1
    var onClose: (() -> Void)?

    override var canBecomeKey: Bool { true }

    init(anchor: NSRect, screen: NSScreen, content: TrayPanel) {
        self.anchor = anchor
        self.visibleFrame = screen.visibleFrame
        super.init(contentRect: .zero, styleMask: [.borderless, .nonactivatingPanel],
                   backing: .buffered, defer: false)
        level = .popUpMenu
        collectionBehavior = [.moveToActiveSpace, .fullScreenAuxiliary, .transient]
        isOpaque = false
        backgroundColor = .clear
        hasShadow = true
        hidesOnDeactivate = false
        isReleasedWhenClosed = false

        material.material = .popover
        material.blendingMode = .behindWindow
        material.state = .active
        material.wantsLayer = true
        contentView = material

        let hosting = TrayHostingView(rootView: content)
        hosting.translatesAutoresizingMaskIntoConstraints = false
        material.addSubview(hosting)
        NSLayoutConstraint.activate([
            hosting.topAnchor.constraint(equalTo: material.topAnchor, constant: Self.tipHeight),
            hosting.leadingAnchor.constraint(equalTo: material.leadingAnchor),
            hosting.trailingAnchor.constraint(equalTo: material.trailingAnchor),
            hosting.bottomAnchor.constraint(equalTo: material.bottomAnchor),
        ])
        hosting.onContentSizeChange = { [weak self] size in self?.resize(to: size) }
        resize(to: hosting.fittingSize)
    }

    private func resize(to size: NSSize) {
        guard size.width > 0, size.height > 0 else { return }
        let panelSize = NSSize(width: size.width, height: size.height + Self.tipHeight)
        let next = Self.panelFrame(anchoredTo: anchor, visibleFrame: visibleFrame, size: panelSize)
        guard frame != next else { return }
        let maskSize = NSSize(width: next.width, height: min(next.height, Self.maskHeight))
        if material.maskImage?.size != maskSize {
            material.maskImage = Self.materialMask(size: maskSize, centerX: anchor.midX - next.minX)
        }
        setFrame(next, display: false)
        displayIfNeeded()
        invalidateShadow()
    }

    private static func materialMask(size: NSSize, centerX: CGFloat) -> NSImage {
        let outline = Self.outline(size: size, centerX: centerX)
        let mask = NSImage(size: size, flipped: false) { _ in
            guard let context = NSGraphicsContext.current?.cgContext else { return false }
            context.setFillColor(NSColor.black.cgColor)
            context.addPath(outline)
            context.fillPath()
            return true
        }
        if size.height == Self.maskHeight {
            // Stretch only the body so height changes preserve the tip and corners.
            mask.capInsets = NSEdgeInsets(top: Self.tipHeight + Self.cornerRadius,
                                          left: 0, bottom: Self.cornerRadius, right: 0)
            mask.resizingMode = .stretch
        }
        return mask
    }

    private static func outline(size: NSSize, centerX: CGFloat) -> CGPath {
        let w = size.width
        let top = size.height - tipHeight
        let r = min(cornerRadius, w / 4, top / 2)
        let tip = min(max(centerX, r + 6), w - r - 6)
        let path = CGMutablePath()
        path.move(to: CGPoint(x: 0, y: r))
        path.addQuadCurve(to: CGPoint(x: r, y: 0), control: .zero)
        path.addLine(to: CGPoint(x: w - r, y: 0))
        path.addQuadCurve(to: CGPoint(x: w, y: r), control: CGPoint(x: w, y: 0))
        path.addLine(to: CGPoint(x: w, y: top - r))
        path.addQuadCurve(to: CGPoint(x: w - r, y: top), control: CGPoint(x: w, y: top))
        path.addLine(to: CGPoint(x: tip + 6, y: top))
        path.addLine(to: CGPoint(x: tip, y: size.height))
        path.addLine(to: CGPoint(x: tip - 6, y: top))
        path.addLine(to: CGPoint(x: r, y: top))
        path.addQuadCurve(to: CGPoint(x: 0, y: top - r), control: CGPoint(x: 0, y: top))
        path.closeSubpath()
        return path
    }

    static func panelFrame(anchoredTo anchor: NSRect, visibleFrame: NSRect, size: NSSize) -> NSRect {
        let bounds = visibleFrame.insetBy(dx: 8, dy: 8)
        let width = min(size.width, bounds.width)
        let x = min(max(anchor.midX - width / 2, bounds.minX), bounds.maxX - width)
        let top = min(anchor.minY, visibleFrame.maxY) - 4
        let height = min(size.height, max(0, top - bounds.minY))
        return NSRect(x: x, y: top - height, width: width, height: height)
    }

    func present() {
        let animate = !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion
        alphaValue = animate ? 0.48 : 1
        makeKeyAndOrderFront(nil)
        if animate {
            NSAnimationContext.runAnimationGroup { context in
                context.duration = 0.16
                context.timingFunction = CAMediaTimingFunction(name: .easeOut)
                animator().alphaValue = 1
            }
        }
    }

    override func cancelOperation(_ sender: Any?) { onClose?() }
}

final class TrayHostingView: NSHostingView<TrayPanel> {
    var onContentSizeChange: ((NSSize) -> Void)?

    override func invalidateIntrinsicContentSize() {
        super.invalidateIntrinsicContentSize()
        Task { @MainActor [weak self] in
            guard let self else { return }
            onContentSizeChange?(fittingSize)
        }
    }
}
