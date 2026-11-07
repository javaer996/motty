import XCTest

/// Drives real swipes against the loopback SSH fixture and reads the in-app
/// latency probe. Start `tools/e2esshd` on 127.0.0.1:2222 first:
///
///     go run ./tools/e2esshd 2222
///
/// The password is "secret"; `UITestSupport` seeds the host at launch. Run
/// with the `MottyUITests` scheme and an iOS Simulator destination.
final class ScrollLatencyUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    /// The gesture that first made the lag visible: a quick swipe into the
    /// scrollback, followed by two slow swipes for comparison.
    func testFirstSwipeLatency() throws {
        let (app, probe) = try connect(autoType: "seq 1 300")
        waitForScrollback(probe, atLeast: 250)

        drag(app, velocity: .fast, pressDuration: 0.01, hold: 0.4,
             from: CGVector(dx: 0.5, dy: 0.30), to: CGVector(dx: 0.5, dy: 0.62))
        let first = try probeValue(probe)
        print("FIRST_SWIPE_FAST \(first)")

        XCTAssertEqual(number(named: "seq", in: first), 1, "first drag was not the first touch: \(first)")
        guard let began = number(named: "began", in: first),
              let draw = number(named: "draw", in: first) else {
            XCTFail("probe did not record the gesture: \(first)")
            return
        }
        XCTAssertLessThan(began, 60, "recognizer took \(began) ms to begin: \(first)")
        XCTAssertLessThan(draw, 80, "first paint took \(draw) ms after the touch: \(first)")

        // A slow drag: the case where the finger moves so little at first that
        // the 3 pt threshold itself dominates.
        drag(app, velocity: .slow, pressDuration: 0.05, hold: 0.6,
             from: CGVector(dx: 0.5, dy: 0.35), to: CGVector(dx: 0.5, dy: 0.65))
        let second = try probeValue(probe)
        print("SECOND_SWIPE_SLOW \(second)")
        XCTAssertEqual(number(named: "seq", in: second), 2, "second drag was not counted: \(second)")

        // Steady state: the same slow drag again.
        drag(app, velocity: .slow, pressDuration: 0.05, hold: 0.6,
             from: CGVector(dx: 0.5, dy: 0.35), to: CGVector(dx: 0.5, dy: 0.65))
        let third = try probeValue(probe)
        print("THIRD_SWIPE_SLOW \(third)")
        XCTAssertEqual(number(named: "seq", in: third), 3, "third drag was not counted: \(third)")
    }

    /// The first swipe while the shell is still flooding the terminal. This is
    /// the busy-main-thread case where touch delivery can trail the finger.
    func testFirstSwipeWhileOutputStreams() throws {
        let (app, probe) = try connect(autoType: "seq 1 500000")
        waitForScrollback(probe, atLeast: 250)

        drag(app, velocity: .fast, pressDuration: 0.01, hold: 0.4,
             from: CGVector(dx: 0.5, dy: 0.30), to: CGVector(dx: 0.5, dy: 0.62))
        let swipe = try probeValue(probe)
        print("SWIPE_UNDER_LOAD \(swipe)")

        XCTAssertEqual(number(named: "seq", in: swipe), 1, "drag was not the first touch: \(swipe)")
        guard let draw = number(named: "draw", in: swipe),
              let move = number(named: "move", in: swipe) else {
            XCTFail("probe did not record the gesture: \(swipe)")
            return
        }
        // These are diagnostics first; the numbers show whether output load is
        // what the user is feeling.
        XCTAssertLessThan(move, 120, "the first move event took \(move) ms to arrive: \(swipe)")
        XCTAssertLessThan(draw, 120, "the first paint took \(draw) ms after the touch: \(swipe)")
    }

    // MARK: Fixture

    private func connect(autoType: String) throws -> (XCUIApplication, XCUIElement) {
        // The simulator reaches the Mac over loopback. A real phone needs the
        // Mac's LAN address; pass it with TEST_RUNNER_MOTTY_UI_TEST_HOST and
        // TEST_RUNNER_MOTTY_UI_TEST_PORT.
        let environment = ProcessInfo.processInfo.environment
        let fixtureHost = environment["MOTTY_UI_TEST_HOST"] ?? "127.0.0.1"
        let fixturePort = environment["MOTTY_UI_TEST_PORT"] ?? "2222"
        let app = XCUIApplication()
        app.launchArguments = [
            "-MottyScrollProbe",
            "-MottyUITestSeed", "motty-ui-fixture", fixtureHost, fixturePort, "me", "secret",
            "-MottyUITestAutoType", autoType,
        ]
        app.launch()

        // The fixture is saved asynchronously after the core starts. Its label
        // is unique to UI tests so an older manual host cannot match.
        let host = app.buttons.matching(
            NSPredicate(format: "label BEGINSWITH %@", "motty-ui-fixture")
        ).firstMatch
        XCTAssertTrue(host.waitForExistence(timeout: 15), "seeded host never appeared")
        host.tap()

        // A freshly installed app has not seen this throwaway server's host key.
        let trust = app.buttons["prompt.trust"]
        if trust.waitForExistence(timeout: 5) {
            trust.tap()
        }

        // Saved passwords are answered from the vault. If the vault write
        // raced the tap, answer the visible prompt instead.
        let password = app.secureTextFields["prompt.password"]
        if password.waitForExistence(timeout: 3) {
            password.tap()
            password.typeText("secret")
            app.buttons["prompt.submit"].tap()
        }

        let probe = app.descendants(matching: .any)
            .matching(identifier: "scrollProbe")
            .firstMatch
        XCTAssertTrue(probe.waitForExistence(timeout: 20), "terminal probe never appeared")
        return (app, probe)
    }

    private func waitForScrollback(_ probe: XCUIElement, atLeast lines: Double) {
        let filled = XCTNSPredicateExpectation(
            predicate: NSPredicate { object, _ in
                guard let element = object as? XCUIElement,
                      let value = element.value as? String,
                      let scrollback = self.number(named: "sb", in: value) else {
                    return false
                }
                return scrollback >= lines
            },
            object: probe
        )
        wait(for: [filled], timeout: 40)
    }

    private func drag(
        _ app: XCUIApplication,
        velocity: XCUIGestureVelocity,
        pressDuration: TimeInterval,
        hold: TimeInterval,
        from: CGVector,
        to: CGVector
    ) {
        let start = app.coordinate(withNormalizedOffset: from)
        let end = app.coordinate(withNormalizedOffset: to)
        start.press(
            forDuration: pressDuration,
            thenDragTo: end,
            withVelocity: velocity,
            thenHoldForDuration: hold
        )
    }

    private func probeValue(_ element: XCUIElement) throws -> String {
        guard let value = element.value as? String, !value.isEmpty else {
            throw XCTSkip("the probe has no value yet")
        }
        return value
    }

    private func number(named name: String, in summary: String) -> Double? {
        for field in summary.split(separator: " ") {
            let parts = field.split(separator: "=", maxSplits: 1)
            guard parts.count == 2, parts[0] == Substring(name) else { continue }
            return Double(parts[1])
        }
        return nil
    }
}
