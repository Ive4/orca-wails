import Foundation

struct TrayRing {
    enum State: Int {
        case running, waiting, blocked
    }

    struct Sector: Equatable {
        let state: State
        let fraction: Double
    }

    struct Frame: Equatable {
        let levels: Levels
        var rotation = 0.0
        var lightAngle: Double?
        var breath = 1.0
        var replyGlow = 0.0

        var sectors: [Sector] {
            let total = levels.running + levels.waiting + levels.blocked
            guard total > 0 else { return [] }
            return [Sector(state: .running, fraction: levels.running / total),
                    Sector(state: .waiting, fraction: levels.waiting / total),
                    Sector(state: .blocked, fraction: levels.blocked / total)].filter { $0.fraction > 0 }
        }
    }

    struct Levels: Equatable {
        var running = 0.0
        var waiting = 0.0
        var blocked = 0.0
        var unconfirmed = 0.0

        var active: Double { max(running, waiting, blocked) }
        var visibility: Double { max(active, unconfirmed) }

        func interpolated(to end: Self, by progress: Double) -> Self {
            Self(running: running + (end.running - running) * progress,
                 waiting: waiting + (end.waiting - waiting) * progress,
                 blocked: blocked + (end.blocked - blocked) * progress,
                 unconfirmed: unconfirmed + (end.unconfirmed - unconfirmed) * progress)
        }
    }

    private static let transitionDuration = 0.24
    private static let rotationDuration = 4.8
    private static let lightDuration = 2.6
    private var source = Levels()
    private var target = Levels()
    private var changedAt: TimeInterval = 0
    private var activeSince: TimeInterval = 0
    private var replyAt: TimeInterval?

    mutating func confirmReply(at time: TimeInterval) { replyAt = time }

    private func replyGlow(at time: TimeInterval) -> Double {
        guard let replyAt else { return 0 }
        return 1 - Self.easeOut((time - replyAt) / 0.38)
    }

    mutating func update(_ lights: TrayLights, at time: TimeInterval, reduceMotion: Bool) {
        let next = Levels(running: lights.running > 0 ? 1 : 0,
                          waiting: lights.waiting > 0 ? 1 : 0,
                          blocked: lights.blocked > 0 ? 1 : 0,
                          unconfirmed: lights.unconfirmed ? 1 : 0)
        guard target != next else { return }
        source = reduceMotion ? next : levels(at: time, reduceMotion: false)
        if target.active == 0, next.active > 0, reduceMotion || source.active == 0 { activeSince = time }
        target = next
        changedAt = time
    }

    private func levels(at time: TimeInterval, reduceMotion: Bool) -> Levels {
        guard !reduceMotion, isTransitioning(at: time) else { return target }
        return source.interpolated(to: target, by: Self.easeOut((time - changedAt) / Self.transitionDuration))
    }

    func frame(at time: TimeInterval, reduceMotion: Bool) -> Frame {
        let levels = levels(at: time, reduceMotion: reduceMotion)
        guard !reduceMotion else { return Frame(levels: levels) }
        let glow = replyGlow(at: time)
        guard levels.active > 0 else { return Frame(levels: levels, replyGlow: glow) }
        let elapsed = time - activeSince
        let ribbon = 2 * Double.pi * (elapsed.truncatingRemainder(dividingBy: Self.rotationDuration) / Self.rotationDuration)
        let light = 2 * Double.pi * (elapsed.truncatingRemainder(dividingBy: Self.lightDuration) / Self.lightDuration)
        let count = [levels.running, levels.waiting, levels.blocked].filter { $0 > 0 }.count
        let breath = count == 1 ? 0.87 + 0.13 * cos(elapsed * 2 * .pi / 3.6) : 1
        return Frame(levels: levels, rotation: count > 1 ? -ribbon + 0.12 * sin(2 * ribbon) : 0,
                     lightAngle: light - .pi / 2, breath: breath, replyGlow: glow)
    }

    func frameInterval(at time: TimeInterval, reduceMotion: Bool) -> TimeInterval {
        guard !reduceMotion else { return 1 }
        return isTransitioning(at: time) || target.active > 0 || replyGlow(at: time) > 0 ? 1.0 / 25 : 1
    }

    private func isTransitioning(at time: TimeInterval) -> Bool {
        source != target && time - changedAt < Self.transitionDuration
    }

    private static func easeOut(_ fraction: Double) -> Double {
        1 - pow(1 - min(1, max(0, fraction)), 3)
    }
}
