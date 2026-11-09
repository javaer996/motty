import UIKit

// Compiled with the real DesktopUIView.swift by test-desktop.sh. Only the Go
// bridge is replaced: gesture handlers and UIKit recognizers are production code.
final class MottyBridge {
    func desktopPointer(desktopID: UInt32, x: Int, y: Int, buttons: UInt8) {}
    func desktopWheel(desktopID: UInt32, x: Int, y: Int, dx: Int, dy: Int) {}
    func desktopKey(desktopID: UInt32, key: String?, rune: String?, typed: String?, down: Bool, repeat: Bool) {}
    func desktopText(desktopID: UInt32, text: String) {}
}

func MottyDesktopFrame(_ id: UInt32, _ version: UInt64, _ buffer: UnsafeMutableRawPointer?, _ capacity: Int32) -> Int32 {
    if id == 999 { return -1 }
    guard version == 0 else { return 0 }
    let needed = 40 + 400 * 300 * 4
    guard Int(capacity) >= needed, let buffer else { return -Int32(needed) }
    buffer.initializeMemory(as: UInt8.self, repeating: 0, count: needed)
    buffer.storeBytes(of: UInt64(1), as: UInt64.self)
    for (offset, value) in [(8, 400), (12, 300), (16, 1), (32, 400), (36, 300)] {
        buffer.storeBytes(of: Int32(value), toByteOffset: offset, as: Int32.self)
    }
    return Int32(needed)
}

private final class Pan: UIPanGestureRecognizer {
    var phase = UIGestureRecognizer.State.possible
    var delta = CGPoint.zero
    var position = CGPoint(x: 200, y: 150)
    var speed = CGPoint.zero
    override var state: UIGestureRecognizer.State {
        get { phase }
        set { phase = newValue }
    }
    override func translation(in view: UIView?) -> CGPoint { delta }
    override func setTranslation(_ translation: CGPoint, in view: UIView?) { delta = translation }
    override func location(in view: UIView?) -> CGPoint { position }
    override func velocity(in view: UIView?) -> CGPoint { speed }
}

private final class Pinch: UIPinchGestureRecognizer {
    var phase = UIGestureRecognizer.State.possible
    override var state: UIGestureRecognizer.State {
        get { phase }
        set { phase = newValue }
    }
    override func location(in view: UIView?) -> CGPoint { CGPoint(x: 200, y: 150) }
}

private final class Tap: UITapGestureRecognizer {
    var position = CGPoint.zero
    override func location(in view: UIView?) -> CGPoint { position }
}

@main
private struct DesktopSurfaceTests {
    static func value<T>(_ name: String, from surface: DesktopSurfaceView) -> T {
        Mirror(reflecting: surface).children.first { $0.label == name }!.value as! T
    }

    static func main() {
        let surface = DesktopSurfaceView(frame: CGRect(x: 0, y: 0, width: 400, height: 300))
        surface.desktopID = 1
        let window = UIWindow(frame: surface.frame)
        window.addSubview(surface)
        defer { surface.stop() }
        let deadline = Date().addingTimeInterval(3)
        while value("pictureWidth", from: surface) as Int == 0 && Date() < deadline {
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        }
        precondition(value("pictureWidth", from: surface) as Int == 400, "fixture frame was not polled")

        var wheels: [(x: Int, y: Int, dx: Int, dy: Int)] = []
        surface.onWheel = { wheels.append(($0, $1, $2, $3)) }
        var inputOrder: [String] = []
        surface.onBeginScroll = { inputOrder.append("release toolbar modifiers") }
        surface.onPointer = { _, _, _ in inputOrder.append("position pointer") }
        let pan = Pan()
        let pinch = Pinch()
        func scroll(_ state: UIGestureRecognizer.State, x: CGFloat = 0, y: CGFloat = 0) {
            pan.phase = state
            pan.delta = CGPoint(x: x, y: y)
            surface.perform(NSSelectorFromString("handleScroll:"), with: pan)
        }
        func zoom(_ state: UIGestureRecognizer.State, scale: CGFloat = 1) {
            pinch.phase = state
            pinch.scale = scale
            surface.perform(NSSelectorFromString("handlePinch:"), with: pinch)
        }

        scroll(.began, y: -13)
        precondition(wheels.count == 1 && wheels[0].dy == 4, "swipe must scroll four times as far as before")
        let anchor = wheels[0]
        precondition(inputOrder == ["release toolbar modifiers", "position pointer"],
                     "pointer input left toolbar Ctrl held or failed to position the cursor")
        // Move outside a small terminal window and even outside the picture.
        // Wheels must still reach the content where the swipe started.
        pan.position = CGPoint(x: 500, y: -50)
        scroll(.changed, x: 25)
        precondition(wheels.last!.dx == 8, "horizontal scrolling was lost")
        precondition(wheels.last!.x == anchor.x && wheels.last!.y == anchor.y,
                     "scroll target followed the fingers out of the terminal window")
        scroll(.ended)
        pan.position = CGPoint(x: 200, y: 150)
        wheels.removeAll()

        // Neither completed nor cancelled gestures may leak fractional travel
        // into the next swipe.
        for end in [UIGestureRecognizer.State.ended, .cancelled] {
            scroll(.began, y: -1.5)
            scroll(end)
            scroll(.began, y: -1)
            scroll(.ended)
        }
        precondition(wheels.isEmpty, "scroll remainder leaked between gestures")

        zoom(.began)
        scroll(.began)
        scroll(.changed, y: -12)
        zoom(.changed, scale: 1.5)
        precondition(value("zoom", from: surface) as CGFloat == 1, "scrolling turned into a pinch")
        scroll(.ended)
        zoom(.ended)
        wheels.removeAll()

        scroll(.began)
        zoom(.began)
        zoom(.changed, scale: 2)
        precondition(value("zoom", from: surface) as CGFloat == 2, "deliberate pinch did not zoom")
        scroll(.changed, y: -24)
        zoom(.ended)
        scroll(.changed, y: -24)
        scroll(.ended)
        precondition(wheels.isEmpty, "pinch or its ending sent a remote wheel")

        scroll(.began, y: -24)
        precondition(wheels.count == 1 && wheels[0].dy == 8, "zoomed picture swallowed remote scrolling")
        precondition(wheels[0].x == 200 && wheels[0].y == 162, "zoomed wheel coordinates were incorrect")
        scroll(.ended)

        pan.phase = .began
        pan.delta = CGPoint(x: 30, y: 20)
        let beforePan: CGPoint = value("pictureOffset", from: surface)
        surface.perform(NSSelectorFromString("handleViewportPan:"), with: pan)
        precondition(value("pictureOffset", from: surface) as CGPoint == CGPoint(x: beforePan.x + 30, y: beforePan.y + 20),
                     "three-finger viewport pan stopped working")
        precondition(wheels.count == 1, "viewport pan sent remote input")

        zoom(.began)
        zoom(.changed, scale: 1.5)
        zoom(.changed, scale: 1)
        precondition(value("zoom", from: surface) as CGFloat == 2, "pinch could not return to its starting zoom")
        zoom(.ended)

        // A fast swipe keeps scrolling after release; a new touch sequence
        // cancels it, so inertia cannot spill into the next interaction.
        wheels.removeAll()
        scroll(.began, y: -12)
        pan.speed = CGPoint(x: 0, y: -800)
        scroll(.ended)
        let beforeMomentum = wheels.count
        RunLoop.main.run(until: Date().addingTimeInterval(0.12))
        precondition(wheels.count > beforeMomentum, "fast swipe had no momentum")
        pan.speed = .zero
        scroll(.began)
        scroll(.cancelled)
        let afterCancellation = wheels.count
        RunLoop.main.run(until: Date().addingTimeInterval(0.12))
        precondition(wheels.count == afterCancellation, "cancelled momentum kept sending wheels")

        // A large displacement is split across ticks, preserving every notch
        // while respecting the backend's ten-notch message bound.
        wheels.removeAll()
        scroll(.began, y: -96)
        scroll(.ended)
        RunLoop.main.run(until: Date().addingTimeInterval(0.15))
        precondition(wheels.reduce(0) { $0 + $1.dy } == 32, "fast swipe travel was dropped")
        precondition(wheels.allSatisfy { abs($0.dx) <= 10 && abs($0.dy) <= 10 },
                     "gesture exceeded the backend wheel limit")
        scroll(.began)
        scroll(.cancelled)

        let recognizers = surface.gestureRecognizers!
        let remotePan = recognizers.compactMap { $0 as? UIPanGestureRecognizer }
            .first { $0.minimumNumberOfTouches == 2 }!
        let viewportPan = recognizers.compactMap { $0 as? UIPanGestureRecognizer }
            .first { $0.minimumNumberOfTouches == 3 }!
        let realPinch = recognizers.first { $0 is UIPinchGestureRecognizer }!
        precondition(surface.gestureRecognizer(remotePan, shouldRecognizeSimultaneouslyWith: realPinch))
        precondition(!surface.gestureRecognizer(viewportPan, shouldRecognizeSimultaneouslyWith: realPinch))

        zoom(.began)
        zoom(.changed, scale: 2)
        zoom(.ended)
        let tap = Tap()
        var pointer = (x: -1, y: -1)
        surface.onPointer = { x, y, _ in pointer = (x, y) }
        // Small/large phones and tablet, both orientations. Insets include
        // asymmetric cutouts and visible toolbar/key-bar rails.
        for (size, insets) in [
            (CGSize(width: 390, height: 844), UIEdgeInsets(top: 107, left: 0, bottom: 78, right: 0)),
            (CGSize(width: 844, height: 390), UIEdgeInsets(top: 0, left: 107, bottom: 21, right: 105)),
            (CGSize(width: 430, height: 932), UIEdgeInsets(top: 107, left: 0, bottom: 78, right: 0)),
            (CGSize(width: 932, height: 430), UIEdgeInsets(top: 0, left: 107, bottom: 21, right: 105)),
            (CGSize(width: 768, height: 1024), UIEdgeInsets(top: 72, left: 0, bottom: 64, right: 0)),
            (CGSize(width: 1024, height: 768), UIEdgeInsets(top: 24, left: 48, bottom: 20, right: 46))
        ] {
            surface.frame = CGRect(origin: .zero, size: size)
            surface.viewportInsets = insets
            surface.layoutIfNeeded()
            let visible = UIEdgeInsets(
                top: max(insets.top, surface.safeAreaInsets.top), left: max(insets.left, surface.safeAreaInsets.left),
                bottom: max(insets.bottom, surface.safeAreaInsets.bottom), right: max(insets.right, surface.safeAreaInsets.right)
            )
            for (horizontal, vertical) in [(1, 1), (-1, 1), (1, -1), (-1, -1)] {
                pan.phase = .changed
                pan.delta = CGPoint(x: horizontal * 10000, y: vertical * 10000)
                surface.perform(NSSelectorFromString("handleViewportPan:"), with: pan)
                tap.position = CGPoint(
                    x: horizontal > 0 ? visible.left + 0.001 : size.width - visible.right - 0.001,
                    y: vertical > 0 ? visible.top + 0.001 : size.height - visible.bottom - 0.001
                )
                surface.perform(NSSelectorFromString("handleTap:"), with: tap)
                precondition(pointer.x == (horizontal > 0 ? 0 : 399) && pointer.y == (vertical > 0 ? 0 : 299),
                             "zoomed desktop corner is hidden by a cutout or control rail at \(size): \(pointer)")
            }
        }
        surface.desktopID = 999
        RunLoop.main.run(until: Date().addingTimeInterval(0.12))
        precondition(value("displayLink", from: surface) as CADisplayLink? == nil,
                     "unknown desktop did not stop polling")
        surface.desktopID = 2
        RunLoop.main.run(until: Date().addingTimeInterval(0.12))
        precondition(value("pictureWidth", from: surface) as Int == 400,
                     "reused surface did not restart frame polling after reconnect")
        print("PASS: scrolling, momentum, modifiers, gesture isolation; zoomed corners reachable in both orientations; reused surface resumes after reconnect")
    }
}
