#if DEBUG
import UIKit

/// Debug-only timings for the first frames of a scroll gesture.
///
/// The UI test needs to know what happens between a touch arriving and the
/// terminal first painting under the finger, which is invisible from the
/// outside. The probe keeps those timestamps and a tiny accessibility element
/// exposes them to XCUITest. It stays completely inert unless the app is
/// launched with `-MottyScrollProbe`.
final class ScrollLatencyProbe {
    static let shared = ScrollLatencyProbe()

    let enabled = ProcessInfo.processInfo.arguments.contains("-MottyScrollProbe")

    private var sequence = 0
    private var touchTime: CFTimeInterval = 0
    private var deliveryTime: CFTimeInterval = 0
    private var firstMoveTime: CFTimeInterval = 0
    private var firstMoveEventTime: CFTimeInterval = 0
    private var beganTime: CFTimeInterval = 0
    private var beganDistance: CGFloat = 0
    private var handlerTime: CFTimeInterval = 0
    private var firstTickTime: CFTimeInterval = 0
    private var firstDrawTime: CFTimeInterval = 0
    private var ackTime: CFTimeInterval = 0
    private var frames = 0
    private var visualDelta: CGFloat = 0
    private var firstDrawVisual: CGFloat = 0
    private var lastVisual: CGFloat = 0
    private var maxVisual: CGFloat = 0
    private var maxLagLines: CGFloat = 0
    private var clampedFrames = 0
    private var pending = false

    /// `timestamp` is the touch's own timestamp, so the difference from the
    /// time `touchesBegan` runs includes main-thread delivery delay.
    func touchDown(at timestamp: CFTimeInterval) {
        guard enabled else { return }
        sequence += 1
        touchTime = timestamp
        deliveryTime = 0
        firstMoveTime = 0
        firstMoveEventTime = 0
        beganTime = 0
        beganDistance = 0
        handlerTime = 0
        firstTickTime = 0
        firstDrawTime = 0
        ackTime = 0
        frames = 0
        visualDelta = 0
        firstDrawVisual = 0
        lastVisual = 0
        maxVisual = 0
        maxLagLines = 0
        clampedFrames = 0
        pending = true
    }

    func touchDelivered(at time: CFTimeInterval) {
        guard enabled, pending, deliveryTime == 0 else { return }
        deliveryTime = time
    }

    /// The first move event after the finger goes down. Comparing its handler
    /// time with `.began` separates event delivery from the recognizer's own
    /// 3 pt threshold.
    func touchMoved(eventTimestamp: CFTimeInterval, at time: CFTimeInterval) {
        guard enabled, pending, firstMoveTime == 0 else { return }
        firstMoveEventTime = eventTimestamp
        firstMoveTime = time
    }

    /// When the recognizer crosses its 3 pt threshold.
    func panBegan(at time: CFTimeInterval, distance: CGFloat) {
        guard enabled, pending, beganTime == 0 else { return }
        beganTime = time
        beganDistance = distance
    }

    /// When the scroll handler receives `.began`.
    func panHandler(at time: CFTimeInterval) {
        guard enabled, pending, handlerTime == 0 else { return }
        handlerTime = time
    }

    /// One display-link frame while the finger is down. `lagLines` is how far
    /// behind the finger the drawn content is after the 6-line clamp.
    func frame(visualOffset: CGFloat, lagLines: CGFloat, at time: CFTimeInterval) {
        guard enabled, pending, beganTime > 0 else { return }
        if firstDrawTime == 0 {
            frames += 1
            if firstTickTime == 0 {
                firstTickTime = time
                visualDelta = visualOffset
            }
        }
        lastVisual = visualOffset
        maxVisual = max(maxVisual, abs(visualOffset))
        maxLagLines = max(maxLagLines, abs(lagLines))
        if lagLines > 0.01 {
            clampedFrames += 1
        }
    }

    /// The first paint that actually applies a non-zero scroll translation.
    func draw(visualOffset: CGFloat, at time: CFTimeInterval) {
        guard enabled, pending, beganTime > 0, firstDrawTime == 0, visualOffset != 0 else { return }
        firstDrawTime = time
        firstDrawVisual = visualOffset
    }

    /// The first snapshot whose whole-line offset moved, proving the core
    /// acknowledged the scroll request.
    func coreAck(at time: CFTimeInterval) {
        guard enabled, pending, beganTime > 0, ackTime == 0 else { return }
        ackTime = time
    }

    private func milliseconds(from reference: CFTimeInterval) -> String {
        guard reference > 0, touchTime > 0 else { return "-" }
        return String(format: "%.1f", (reference - touchTime) * 1000)
    }

    private func milliseconds(from start: CFTimeInterval, to end: CFTimeInterval) -> String {
        guard start > 0, end > 0 else { return "-" }
        return String(format: "%.1f", (end - start) * 1000)
    }

    var summary: String {
        guard enabled else { return "" }
        guard sequence > 0 else { return "seq=0" }
        let delta = String(format: "%.1f", visualDelta)
        let distance = String(format: "%.1f", beganDistance)
        let eventLag = firstMoveTime > 0 && firstMoveEventTime > 0
            ? String(format: "%.1f", (firstMoveTime - firstMoveEventTime) * 1000)
            : "-"
        return "seq=\(sequence) deliver=\(milliseconds(from: deliveryTime))"
            + " move=\(milliseconds(from: firstMoveTime))"
            + " moveLag=\(eventLag)"
            + " began=\(milliseconds(from: beganTime))"
            + " beganAfterMove=\(milliseconds(from: firstMoveTime, to: beganTime))"
            + " beganDistance=\(distance)"
            + " handler=\(milliseconds(from: handlerTime))"
            + " tick=\(milliseconds(from: firstTickTime))"
            + " draw=\(milliseconds(from: firstDrawTime))"
            + " ack=\(milliseconds(from: ackTime))"
            + " frames=\(frames) delta=\(delta)"
            + " drawVisual=\(String(format: "%.1f", firstDrawVisual))"
            + " lastVisual=\(String(format: "%.1f", lastVisual))"
            + " maxVisual=\(String(format: "%.1f", maxVisual))"
            + " maxLag=\(String(format: "%.1f", maxLagLines))"
            + " clampFrames=\(clampedFrames)"
    }
}

/// A transparent, non-interactive view that only exists to carry the probe
/// summary out through the accessibility tree.
final class ScrollProbeAccessibilityView: UIView {
    var summary: (() -> String)?

    override var isAccessibilityElement: Bool {
        get { true }
        set {}
    }

    override var accessibilityValue: String? {
        get { summary?() ?? "" }
        set {}
    }
}
#endif
