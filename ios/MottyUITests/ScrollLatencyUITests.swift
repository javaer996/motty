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

    /// At the live screen (viewOffset 0) an upward swipe used to be damped to
    /// 30% and hard-capped at one line, so it barely moved. It has to follow
    /// the finger past the end and then spring back.
    func testSwipeUpAtBottomFollowsFinger() throws {
        let (app, probe) = try connect(autoType: "seq 1 300")
        waitForScrollback(probe, atLeast: 250)
        waitForBottom(probe)

        drag(app, velocity: .fast, pressDuration: 0.01, hold: 0.5,
             from: CGVector(dx: 0.5, dy: 0.50), to: CGVector(dx: 0.5, dy: 0.15))
        let swipe = try probeValue(probe)
        print("SWIPE_UP_AT_BOTTOM \(swipe)")

        guard let drawVisual = number(named: "drawVisual", in: swipe),
              let lastVisual = number(named: "lastVisual", in: swipe) else {
            XCTFail("probe did not record the gesture: \(swipe)")
            return
        }
        XCTAssertLessThan(drawVisual, -3,
                          "the first paint did not follow the upward swipe: \(swipe)")
        XCTAssertLessThan(lastVisual, -40,
                          "the bottom bounce stopped following the finger: \(swipe)")
        XCTAssertEqual(number(named: "off", in: swipe), 0,
                       "the core should stay at the live screen: \(swipe)")

        // Releasing past the end springs back to the live screen.
        let settled = XCTNSPredicateExpectation(
            predicate: NSPredicate { object, _ in
                guard let element = object as? XCUIElement,
                      let value = element.value as? String,
                      let visual = self.number(named: "vis", in: value) else {
                    return false
                }
                return abs(visual) < 1
            },
            object: probe
        )
        wait(for: [settled], timeout: 5)
    }

    /// The terminal focuses itself on appear, so the keyboard is up from the
    /// first frame. It has to shrink (and resize the pty) instead of drawing
    /// its last rows behind the input method.
    func testTerminalShrinksForKeyboard() throws {
        let (_, probe) = try connect(autoType: "seq 1 300")
        waitForScrollback(probe, atLeast: 250)

        let summary = try probeValue(probe)
        print("KEYBOARD_ROWS \(summary)")
        guard let rows = number(named: "rows", in: summary) else {
            XCTFail("probe did not report rows: \(summary)")
            return
        }
        XCTAssertGreaterThan(rows, 5, "the terminal collapsed: \(summary)")
        XCTAssertLessThan(rows, 30, "the terminal did not shrink for the keyboard: \(summary)")
    }

    /// Typing a command prefix has to offer a common parameter combination
    /// inline, above the keyboard, and tapping it types the rest of the line.
    func testInlineCompletionSuggestsACombination() throws {
        let (app, probe) = try connect(autoType: "")

        // The terminal is a UIKeyInput view; a tap makes sure it is the first
        // responder before the keyboard types into it.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).tap()
        app.typeText("git st")

        let chip = app.buttons["suggestion.0"]
        XCTAssertTrue(chip.waitForExistence(timeout: 10), "no inline suggestion appeared")
        XCTAssertTrue(
            chip.label.contains("git status -sb"),
            "the first suggestion did not show the full combination: \(chip.label)"
        )

        // A plain tap makes XCUITest scroll the chip into view first, which
        // fails inside the keyboard's accessory view; tapping its coordinate
        // hits it directly.
        chip.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()

        // The probe reports the line the core read back from the screen, so a
        // match proves the suffix reached the shell and came back.
        let inserted = XCTNSPredicateExpectation(
            predicate: NSPredicate { object, _ in
                guard let element = object as? XCUIElement,
                      let value = element.value as? String,
                      let line = self.string(named: "line", in: value) else {
                    return false
                }
                return line == "git_status_-sb"
            },
            object: probe
        )
        wait(for: [inserted], timeout: 10)

        // The line is now exactly a curated combination, so the core has
        // nothing left to offer and the bar hides.
        let gone = XCTNSPredicateExpectation(
            predicate: NSPredicate { _, _ in !chip.exists },
            object: nil
        )
        wait(for: [gone], timeout: 10)
    }

    /// The imported catalog does not know netstat at all; the hand-written
    /// essentials have to offer its common options after the command is typed.
    func testCompletionOffersNetstatOptions() throws {
        let (app, _) = try connect(autoType: "")

        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).tap()
        app.typeText("netstat -")

        let chip = app.buttons["suggestion.0"]
        XCTAssertTrue(chip.waitForExistence(timeout: 10), "no netstat option appeared")
        // The chip shows the complete option while the dash stays typed. The
        // table is ordered by usefulness: a (all) comes first, then t (TCP).
        XCTAssertTrue(
            chip.label.hasPrefix("-a"),
            "the first netstat option was not -a: \(chip.label)"
        )
        XCTAssertTrue(
            app.buttons["suggestion.1"].label.hasPrefix("-t"),
            "the second netstat option was not -t: \(app.buttons["suggestion.1"].label)"
        )
    }

    /// An unfinished command word lists every match, not just the first
    /// screenful; the row scrolls. "c" matches far more than eight commands.
    func testCompletionListsEveryMatchingCommand() throws {
        let (app, _) = try connect(autoType: "")

        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).tap()
        app.typeText("c")

        XCTAssertTrue(app.buttons["suggestion.7"].waitForExistence(timeout: 10),
                      "the suggestion row stopped before the first screenful")
        XCTAssertTrue(app.buttons["suggestion.8"].waitForExistence(timeout: 10),
                      "the suggestion row stopped at eight chips")
    }

    /// Without a space the word is still unfinished: "ls" lists "lsof",
    /// "lsblk" and the other longer commands. Once the space is typed, the
    /// common combinations take over.
    func testCompletionListsLongerCommandsUntilTheSpace() throws {
        let (app, _) = try connect(autoType: "")

        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).tap()
        app.typeText("ls")

        // The chips show complete command names, so lsof is visible even
        // though only "of" would be typed when it is picked.
        let lsof = app.buttons.matching(
            NSPredicate(format: "label BEGINSWITH %@", "lsof")
        ).firstMatch
        XCTAssertTrue(lsof.waitForExistence(timeout: 10),
                      "ls did not offer lsof")
        let lsblk = app.buttons.matching(
            NSPredicate(format: "label BEGINSWITH %@", "lsblk")
        ).firstMatch
        XCTAssertTrue(lsblk.waitForExistence(timeout: 10),
                      "ls did not offer lsblk")

        app.typeText(" ")
        let combo = app.buttons["suggestion.0"]
        XCTAssertTrue(combo.waitForExistence(timeout: 10),
                      "no ls combination appeared after the space")
        XCTAssertTrue(combo.label.hasPrefix("ls -lah"),
                      "the first ls combination was not ls -lah: \(combo.label)")
    }

    /// Picking a command name types the separating space too, so the
    /// command's combinations appear without another keystroke.
    func testPickingACommandContinuesWithItsCombinations() throws {
        let (app, probe) = try connect(autoType: "")

        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).tap()
        app.typeText("netsta")

        let command = app.buttons["suggestion.0"]
        XCTAssertTrue(command.waitForExistence(timeout: 10),
                      "netstat was not offered")
        XCTAssertTrue(command.label.hasPrefix("netstat"),
                      "the first candidate was not netstat: \(command.label)")
        command.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()

        // The core reads the tracked line back, so the underscore proves the
        // space was typed together with the missing suffix.
        let spaced = XCTNSPredicateExpectation(
            predicate: NSPredicate { object, _ in
                guard let element = object as? XCUIElement,
                      let value = element.value as? String,
                      let line = self.string(named: "line", in: value) else {
                    return false
                }
                return line == "netstat_"
            },
            object: probe
        )
        wait(for: [spaced], timeout: 10)

        // The bar switched from the command name to its common combinations.
        let combo = app.buttons["suggestion.0"]
        let ready = XCTNSPredicateExpectation(
            predicate: NSPredicate { _, _ in
                combo.exists && combo.label.hasPrefix("netstat -tulnp")
            },
            object: nil
        )
        wait(for: [ready], timeout: 10)
    }

    /// Several assistant providers can be configured and the active one
    /// switched. This walks the real settings screen, so the Swift form, the
    /// bridge and the Go save all have to agree on the provider list shape.
    func testAssistantProviderCanBeAdded() throws {
        let app = XCUIApplication()
        let environment = ProcessInfo.processInfo.environment
        app.launchArguments = [
            "-MottyUITestSeed", "motty-ui-fixture",
            environment["MOTTY_UI_TEST_HOST"] ?? "127.0.0.1",
            environment["MOTTY_UI_TEST_PORT"] ?? "2222",
            "me", "secret",
        ]
        app.launch()

        app.buttons["settings"].tap()
        let assistant = app.buttons["settings.assistant"]
        XCTAssertTrue(assistant.waitForExistence(timeout: 10), "the assistant row is missing")
        assistant.tap()

        let add = app.buttons.matching(identifier: "ai.addProvider").firstMatch
        XCTAssertTrue(add.waitForExistence(timeout: 10), "the provider list did not appear")
        add.tap()

        let name = app.textFields["ai.name"]
        XCTAssertTrue(name.waitForExistence(timeout: 10), "the provider editor did not appear")
        name.tap()
        name.typeText("Work")
        let model = app.textFields["ai.model"]
        model.tap()
        model.typeText("gpt-test")
        app.buttons["ai.done"].tap()

        XCTAssertTrue(app.staticTexts["Work"].waitForExistence(timeout: 10),
                      "the provider was not added")

        // Saving crosses the bridge and the core; a wrong wire shape leaves
        // an error alert instead of popping back to Settings with the new
        // active provider.
        app.buttons["ai.save"].tap()
        let summary = app.staticTexts.matching(
            NSPredicate(format: "label CONTAINS %@", "gpt-test")
        ).firstMatch
        XCTAssertTrue(summary.waitForExistence(timeout: 10),
                      "the saved provider is not shown in Settings")
    }

    /// The privacy screen crosses the bridge three ways: the switch is saved,
    /// custom words live in the vault, and the dry run tokenizes a draft in
    /// the Go core. Reopening the screen proves the saved state came back.
    func testAssistantPrivacyRoundTrip() throws {
        let app = XCUIApplication()
        let environment = ProcessInfo.processInfo.environment
        app.launchArguments = [
            "-MottyUITestSeed", "motty-ui-fixture",
            environment["MOTTY_UI_TEST_HOST"] ?? "127.0.0.1",
            environment["MOTTY_UI_TEST_PORT"] ?? "2222",
            "me", "secret",
        ]
        app.launch()

        app.buttons["settings"].tap()
        let assistant = app.buttons["settings.assistant"]
        XCTAssertTrue(assistant.waitForExistence(timeout: 10), "the assistant row is missing")
        assistant.tap()

        let privacy = app.buttons["ai.privacy"]
        XCTAssertTrue(privacy.waitForExistence(timeout: 10), "the privacy row is missing")
        privacy.tap()

        let toggle = app.switches["ai.privacy.enabled"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), "the privacy screen did not appear")
        XCTAssertEqual(toggle.value as? String, "1", "protection is off by default")
        // The accessibility frame is the whole Form row; only the control at
        // the trailing edge flips the switch when tapped.
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.5)).tap()
        let off = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "value == %@", "0"),
            object: toggle
        )
        wait(for: [off], timeout: 5)
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.92, dy: 0.5)).tap()
        let on = XCTNSPredicateExpectation(
            predicate: NSPredicate(format: "value == %@", "1"),
            object: toggle
        )
        wait(for: [on], timeout: 5)

        let wordField = app.textFields["ai.privacy.word"]
        XCTAssertTrue(wordField.waitForExistence(timeout: 10), "custom word field is missing")
        wordField.tap()
        wordField.typeText("Acme Internal")
        app.buttons["ai.privacy.add"].tap()
        XCTAssertTrue(app.staticTexts["Acme Internal"].waitForExistence(timeout: 10),
                      "the custom word was not added")

        // The row was written to the vault through Go. Leave and come back:
        // only a successful round trip can show it again.
        app.buttons["ai.privacy.done"].tap()
        let reopened = app.buttons["ai.privacy"]
        XCTAssertTrue(reopened.waitForExistence(timeout: 10))
        reopened.tap()
        XCTAssertTrue(app.staticTexts["Acme Internal"].waitForExistence(timeout: 10),
                      "the custom word did not survive reopening")

        let draft = app.textViews["ai.privacy.draft"]
        XCTAssertTrue(draft.waitForExistence(timeout: 10), "the draft editor is missing")
        draft.tap()
        draft.typeText("ssh alice@192.168.1.20")
        let preview = app.buttons["ai.privacy.preview"]
        XCTAssertTrue(preview.waitForExistence(timeout: 10))
        preview.tap()
        XCTAssertTrue(app.staticTexts["ai.privacy.preview.result"].waitForExistence(timeout: 10),
                      "the dry run produced no result")
        XCTAssertTrue(app.staticTexts["ai.privacy.preview.replacement"].firstMatch
                        .waitForExistence(timeout: 10),
                      "the dry run reported no replacements")
    }

    /// Backgrounds the app for longer than the SSH keepalive interval. The
    /// silent-audio keepalive has to keep the process (and the socket) alive;
    /// if iOS suspends it, the session is gone when the app comes back.
    func testBackgroundKeepAlive() throws {
        let (app, probe) = try connect(autoType: "seq 1 300")
        waitForScrollback(probe, atLeast: 250)
        waitForBottom(probe)

        XCUIDevice.shared.press(.home)
        Thread.sleep(forTimeInterval: 45)

        let screenshot = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        screenshot.name = "Home screen with Live Activity"
        screenshot.lifetime = .keepAlways
        add(screenshot)

        app.activate()
        let alive = XCTNSPredicateExpectation(
            predicate: NSPredicate { object, _ in
                guard let element = object as? XCUIElement,
                      let value = element.value as? String,
                      let phase = self.string(named: "phase", in: value) else {
                    return false
                }
                return phase == "connected"
            },
            object: probe
        )
        wait(for: [alive], timeout: 20)
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

    private func waitForBottom(_ probe: XCUIElement) {
        let bottom = XCTNSPredicateExpectation(
            predicate: NSPredicate { object, _ in
                guard let element = object as? XCUIElement,
                      let value = element.value as? String,
                      let offset = self.number(named: "off", in: value) else {
                    return false
                }
                return offset == 0
            },
            object: probe
        )
        wait(for: [bottom], timeout: 10)
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

    private func string(named name: String, in summary: String) -> String? {
        for field in summary.split(separator: " ") {
            let parts = field.split(separator: "=", maxSplits: 1)
            guard parts.count == 2, parts[0] == Substring(name) else { continue }
            return String(parts[1])
        }
        return nil
    }
}
