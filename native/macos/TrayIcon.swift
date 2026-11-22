import AppKit

@MainActor
enum TrayIcon {
    struct Frame: Equatable {
        let dark: Bool
        let ring: TrayRing.Frame
    }

    private typealias RGB = SIMD3<Double>
    private static let turn = 2 * Double.pi
    private static let size = NSSize(width: 22, height: 22)
    private static let radius = 9.05
    private static let colorSpace = CGColorSpace(name: CGColorSpace.sRGB)!
    private static let standard = Raster(scale: 2)
    private static let magnified = Raster(scale: 4)

    private struct Accent {
        let base: RGB
        let light: RGB

        nonisolated init(_ base: UInt32, _ light: UInt32) {
            self.base = TrayIcon.rgb(base)
            self.light = TrayIcon.rgb(light)
        }

        func mixed(toward other: Self, by amount: Double) -> Self {
            Self(base: base + (other.base - base) * amount, light: light + (other.light - light) * amount)
        }

        private init(base: RGB, light: RGB) {
            self.base = base
            self.light = light
        }
    }

    private static let lightInk = rgb(0x292e38)
    private static let darkInk = rgb(0xf5f8ff)
    private static let lightAccents = [Accent(0x0753bf, 0x45bdff), Accent(0x874700, 0xf5ac41), Accent(0xaa1740, 0xff829a)]
    private static let darkAccents = [Accent(0x369dfc, 0xc4f2ff), Accent(0xf4ab3d, 0xffefb1), Accent(0xfb637f, 0xffc6d4)]

    @MainActor
    private struct Spectrum {
        struct Slice {
            let start: Double
            let end: Double
            let accent: Accent
        }

        let slices: [Slice]
        let angle: Double

        init(_ frame: Frame) {
            let palette = frame.dark ? darkAccents : lightAccents
            let sectors = frame.ring.sectors
            // Center the running color at the bottom when the ring is stationary.
            let runningFraction = sectors.first { $0.state == .running }?.fraction ?? 0
            angle = frame.ring.rotation - .pi / 2 - .pi * runningFraction
            var end = 0.0
            slices = sectors.compactMap { sector in
                let start = end
                end += sector.fraction
                guard end > start else { return nil }
                return Slice(start: start, end: end, accent: palette[sector.state.rawValue])
            }
        }

        func accent(at angle: Double) -> Accent? {
            guard let first = slices.first else { return nil }
            guard slices.count > 1 else { return first.accent }
            let turn = unit((angle - self.angle) / TrayIcon.turn)
            let index = slices.firstIndex { turn < $0.end } ?? slices.count - 1
            let slice = slices[index]
            let previous = slices[(index + slices.count - 1) % slices.count]
            let next = slices[(index + 1) % slices.count]
            // Narrow colors keep narrow joins while entering or leaving the ring.
            let left = 0.13 * min(slice.end - slice.start, previous.end - previous.start)
            let right = 0.13 * min(slice.end - slice.start, next.end - next.start)
            if turn - slice.start < left {
                return slice.accent.mixed(toward: previous.accent, by: 0.5 * clamp(1 - (turn - slice.start) / left))
            }
            if slice.end - turn < right {
                return slice.accent.mixed(toward: next.accent, by: 0.5 * clamp(1 - (slice.end - turn) / right))
            }
            return slice.accent
        }
    }

    @MainActor
    private struct Raster {
        struct Sample {
            let offset: Int
            let angle: Double
            let twist: Double
            let innerRim: Double
            let fill: Double
            let dashedFill: Double
            let halo: Double
        }

        let side: Int
        let samples: [Sample]

        init(scale: Int) {
            side = Int(TrayIcon.size.width) * scale
            let density = Double(scale)
            let dashCount = 8.0
            let dashLength = turn * radius / dashCount
            var samples: [Sample] = []
            for row in 0..<side {
                for column in 0..<side {
                    let x = (Double(column) + 0.5 - Double(side) / 2) / density
                    let y = (Double(side) / 2 - Double(row) - 0.5) / density
                    let offset = hypot(x, y) - radius
                    let halo = clamp((1.82 - abs(offset)) * density + 0.5)
                    guard halo > 0 else { continue }
                    let angle = atan2(y, x)
                    let fill = clamp((1.32 - abs(offset)) * density + 0.5)
                    let dashPhase = abs(unit(angle * dashCount / turn + 0.5) - 0.5)
                    let dash = clamp((0.34 - dashPhase) * dashLength * density + 0.5)
                    samples.append(Sample(offset: (row * side + column) * 4, angle: angle,
                                          twist: 0.28 * offset, innerRim: bell((offset + 0.65) / 0.6),
                                          fill: fill, dashedFill: fill * dash, halo: halo))
                }
            }
            self.samples = samples
        }

        func image(_ frame: TrayRing.Frame, spectrum: Spectrum, ink: RGB) -> CGImage? {
            let active = frame.levels.active
            let opacity = active * frame.breath
            let neutral = 0.62 * frame.levels.unconfirmed
            guard opacity > 0 || neutral > 0 || frame.replyGlow > 0 else { return nil }
            let replyLight = (frame.replyGlow > 0 ? (ink + RGB(0.23, 0.58, 1)) * 0.5 : ink)
            var pixels = [UInt8](repeating: 0, count: side * side * 4)
            for sample in samples {
                let activeFill = sample.fill * opacity
                let neutralFill = sample.dashedFill * neutral * (1 - activeFill)
                let fill = activeFill + neutralFill
                var premultiplied = ink * neutralFill
                var glow = 0.0
                if let accent = spectrum.accent(at: sample.angle + sample.twist) {
                    let reflection = cos(sample.angle - frame.rotation)
                    let sheen = 0.06 + 0.07 * reflection * reflection
                    let pool = frame.lightAngle.map { Self.glint(at: sample.angle, head: $0) } ?? 0
                    let lit = accent.base + (accent.light - accent.base) * (sheen + (1 - sheen) * pool * sample.innerRim)
                    glow = 0.72 * pool * sample.halo * active * (1 - fill)
                    premultiplied += lit * activeFill + accent.light * glow
                }
                let reply = 0.56 * frame.replyGlow * sample.halo * (1 - fill - glow)
                premultiplied += replyLight * reply
                pixels[sample.offset] = byte(premultiplied.x)
                pixels[sample.offset + 1] = byte(premultiplied.y)
                pixels[sample.offset + 2] = byte(premultiplied.z)
                pixels[sample.offset + 3] = byte(fill + glow + reply)
            }
            guard let provider = CGDataProvider(data: Data(pixels) as CFData) else { return nil }
            return CGImage(width: side, height: side, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: side * 4,
                           space: colorSpace, bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue),
                           provider: provider, decode: nil, shouldInterpolate: true, intent: .defaultIntent)
        }

        private static func glint(at angle: Double, head: Double) -> Double {
            let behind = unit((head - angle) / turn) * turn
            let tail = exp(-behind / 0.86) * clamp(1 - behind / 1.88)
            return clamp(0.65 * tail + 0.96 * bell(distance(angle, head) / 0.38))
        }
    }

    static func image(_ mark: NSImage?, frame: Frame) -> NSImage {
        NSImage(size: size, flipped: false) { _ in
            guard let context = NSGraphicsContext.current?.cgContext else { return false }
            let ink = frame.dark ? darkInk : lightInk
            let spectrum = Spectrum(frame)
            let bounds = CGRect(origin: .zero, size: size)
            let backing = context.convertToDeviceSpace(CGSize(width: 1, height: 1))
            let raster = max(abs(backing.width), abs(backing.height)) > 2.01 ? magnified : standard
            if let ring = raster.image(frame.ring, spectrum: spectrum, ink: ink) {
                context.draw(ring, in: bounds)
            }

            if let mark {
                context.saveGState()
                context.translateBy(x: bounds.midX, y: bounds.midY)
                paintMark(mark, frame: frame, spectrum: spectrum, ink: ink, in: context)
                context.restoreGState()
            }
            return true
        }
    }

    private static func paintMark(_ mark: NSImage, frame: Frame, spectrum: Spectrum, ink: RGB, in context: CGContext) {
        let visibility = frame.ring.levels.visibility
        let width = 19 - 4.55 * visibility
        let height = width * mark.size.height / mark.size.width
        let sweep = frame.ring.lightAngle.map { distance($0, -.pi / 2) }
        let contact = sweep.map { bell($0 / 0.85) * frame.ring.levels.active } ?? 0
        let lift = 0.55 * visibility + 0.28 * contact
        let rect = CGRect(x: -width / 2, y: -height / 2 + lift, width: width, height: height)

        context.beginTransparencyLayer(auxiliaryInfo: nil)
        mark.draw(in: rect)
        context.setBlendMode(.sourceIn)
        context.setFillColor(color(ink))
        context.fill(rect)
        if let angle = frame.ring.lightAngle, let sweep, let accent = spectrum.accent(at: angle) {
            context.setBlendMode(.sourceAtop)
            let light = frame.dark ? accent.light : ink + (accent.light - ink) * 0.68
            let x = 5.8 * cos(angle)
            if let gleam = gradient(light, opacity: 0.79 * contact, centered: true) {
                context.drawLinearGradient(gleam, start: CGPoint(x: x - 4, y: -0.9),
                                           end: CGPoint(x: x + 4, y: 0.9), options: [])
            }
            for side in [-1.0, 1.0] {
                let strength = bell((sweep - side * 0.24) / 0.26) * frame.ring.levels.active
                if strength > 0.02, let glow = gradient(light, opacity: 0.88 * strength) {
                    let ear = CGPoint(x: side * 4.6, y: 4.6)
                    context.drawRadialGradient(glow, startCenter: ear, startRadius: 0, endCenter: ear, endRadius: 2.5, options: [])
                }
            }
        }
        context.endTransparencyLayer()
    }

    private static func gradient(_ tint: RGB, opacity: Double, centered: Bool = false) -> CGGradient? {
        let clear = color(tint, alpha: 0)
        let bright = color(tint, alpha: opacity)
        let colors = centered ? [clear, bright, clear] : [bright, clear]
        return CGGradient(colorsSpace: colorSpace, colors: colors as CFArray, locations: nil)
    }

    private static func distance(_ angle: Double, _ reference: Double) -> Double {
        (unit((angle - reference) / turn + 0.5) - 0.5) * turn
    }

    private static func unit(_ value: Double) -> Double { value - floor(value) }
    private static func clamp(_ value: Double) -> Double { min(1, max(0, value)) }
    private static func bell(_ value: Double) -> Double { exp(-value * value) }
    private static func byte(_ value: Double) -> UInt8 { UInt8(clamping: Int((value * 255).rounded())) }

    private static func color(_ rgb: RGB, alpha: Double = 1) -> CGColor {
        CGColor(srgbRed: rgb.x, green: rgb.y, blue: rgb.z, alpha: alpha)
    }

    nonisolated private static func rgb(_ hex: UInt32) -> RGB {
        RGB(Double((hex >> 16) & 0xff), Double((hex >> 8) & 0xff), Double(hex & 0xff)) / 255
    }
}
