import UIKit

/// Draws the cell grid the Go terminal produces and sends input back.
///
/// The Go side is the only terminal state: this view asks for a snapshot when
/// its version moved, decodes it, and paints. It never parses escape
/// sequences and never keeps its own screen.
final class TerminalUIView: UIView, UIKeyInput {
    private let sessionID: UInt32
    private weak var bridge: MottyBridge?

    private let fontSize: CGFloat = 13
    private lazy var regularFont = UIFont.monospacedSystemFont(ofSize: fontSize, weight: .regular)
    private lazy var boldFont = UIFont.monospacedSystemFont(ofSize: fontSize, weight: .bold)
    private var cellWidth: CGFloat = 8
    private var cellHeight: CGFloat = 16
    /// SF Mono's advance is fractional (about 8.04 pt at 13). Text drawn with
    /// the font advances by that exact width, so a cell grid rounded up to
    /// whole points would leave the cursor further right of the glyphs on
    /// every column. Each run is kerned by this amount to stay on the grid.
    private var regularKern: CGFloat = 0
    private var boldKern: CGFloat = 0

    private var displayLink: CADisplayLink?
    private var lastVersion = UInt64.max
    private var snapshot = ScreenSnapshot()
    private var buffer = [UInt8](repeating: 0, count: 512 * 1024)
    private var currentCols = 0
    private var currentRows = 0
    private var inputBar: TerminalInputBar?

    /// Inline command completion: a debounced question to the core after every
    /// keystroke. The core reads the line being edited from the terminal, so
    /// the front end never has to keep one.
    private var completionWork: DispatchWorkItem?
    private var completionSequence = 0
    /// The line the core last read from the screen, for the DEBUG probe.
    private var lastCompletionLine = ""

    // Scrolling is pixel-accurate under the finger while the Go terminal only
    // stores whole-line offsets. `scrollBaseOffset` and `scrollDistance` describe
    // the continuous offset; every frame the newest target is committed, and
    // the difference between the snapshot and that target is drawn as a
    // fractional translation. This keeps a fling from stepping line by line.
    private var isPanning = false
    private var scrollActive = false
    private var scrollBaseOffset: CGFloat = 0
    private var scrollDistance: CGFloat = 0
    private var scrollVelocity: CGFloat = 0
    private var requestedScrollOffset = 0
    private var settleTarget: Int?
    private var settleFrames = 0
    private var bounceBack = false
    private var bounceTargetDistance: CGFloat = 0
    private var lastVisualOffset: CGFloat = 0
    private var lastTickTime: CFTimeInterval = 0

    private struct CellPosition {
        var row: Int
        var column: Int
    }

    /// A text selection made with a long press, in view cells. The anchor is
    /// where the press began; the focus follows the finger.
    private var selectionAnchor: CellPosition?
    private var selectionFocus: CellPosition?

    #if DEBUG
    private var scrollProbeView: ScrollProbeAccessibilityView?
    /// A command typed into the shell as soon as the first prompt appears.
    /// UI tests use it to fill the scrollback without needing keyboard focus.
    private lazy var autoTypeCommand: String? = {
        let arguments = ProcessInfo.processInfo.arguments
        guard let index = arguments.firstIndex(of: "-MottyUITestAutoType"),
              index + 1 < arguments.count else { return nil }
        return arguments[index + 1]
    }()
    private var autoTypeSent = false
    #endif

    private let terminalBackground = UIColor(red: 0.05, green: 0.055, blue: 0.075, alpha: 1)
    private let terminalForeground = UIColor(white: 0.88, alpha: 1)

    private lazy var bottomButton: UIButton = {
        let button = UIButton(type: .system)
        button.setImage(UIImage(systemName: "arrow.down.to.line"), for: .normal)
        button.tintColor = terminalForeground
        button.backgroundColor = UIColor.black.withAlphaComponent(0.55)
        button.layer.cornerRadius = 17
        button.isHidden = true
        button.addTarget(self, action: #selector(jumpToBottom), for: .touchUpInside)
        return button
    }()

    /// Appears while a long-press selection exists; tapping it copies the
    /// selected cells and clears the highlight.
    private lazy var copySelectionButton: UIButton = {
        let button = UIButton(type: .system)
        button.setImage(UIImage(systemName: "doc.on.doc"), for: .normal)
        button.tintColor = terminalForeground
        button.backgroundColor = UIColor.black.withAlphaComponent(0.55)
        button.layer.cornerRadius = 17
        button.isHidden = true
        button.accessibilityLabel = AppLanguage.localized("copy")
        button.addTarget(self, action: #selector(copySelectionTapped), for: .touchUpInside)
        return button
    }()

    init(sessionID: UInt32, bridge: MottyBridge) {
        self.sessionID = sessionID
        self.bridge = bridge
        super.init(frame: .zero)
        isOpaque = true
        backgroundColor = terminalBackground
        contentMode = .redraw
        computeCellSize()

        let tap = UITapGestureRecognizer(target: self, action: #selector(focus))
        addGestureRecognizer(tap)
        let pan = TerminalScrollPanGestureRecognizer(target: self, action: #selector(handlePan(_:)))
        addGestureRecognizer(pan)
        let longPress = UILongPressGestureRecognizer(target: self, action: #selector(handleLongPress(_:)))
        longPress.minimumPressDuration = 0.35
        longPress.cancelsTouchesInView = true
        addGestureRecognizer(longPress)
        // Do not make the pan wait for the long press to fail: that would add
        // its 0.35 s minimum duration to the start of every scroll. A moving
        // finger makes the long press fail immediately, while a still finger
        // never starts the pan and can extend a selection.

        addSubview(bottomButton)
        addSubview(copySelectionButton)

        #if DEBUG
        if ScrollLatencyProbe.shared.enabled {
            let probe = ScrollProbeAccessibilityView(frame: CGRect(x: 0, y: 0, width: 44, height: 44))
            probe.accessibilityIdentifier = "scrollProbe"
            probe.accessibilityLabel = "scrollProbe"
            probe.isUserInteractionEnabled = false
            probe.isOpaque = false
            probe.backgroundColor = .clear
            probe.summary = { [weak self] in self?.scrollProbeSummary ?? "" }
            addSubview(probe)
            scrollProbeView = probe
        }
        #endif

        let bar = TerminalInputBar()
        bar.keyBar.onKey = { [weak self] data in
            self?.send(data)
        }
        bar.keyBar.onPaste = { [weak self] in
            self?.pasteText(UIPasteboard.general.string ?? "")
        }
        bar.keyBar.onCopy = { [weak self] in
            self?.copyScreen()
        }
        bar.onSuggestion = { [weak self] candidate in
            guard let self, !candidate.insert.isEmpty else { return }
            self.send(Data(candidate.insert.utf8))
            // The screen has not echoed yet; ask again once it has.
            self.scheduleCompletion(after: 0.35)
        }
        bar.secondaryKeys.onKey = { [weak self] data in
            self?.send(data)
        }
        bar.secondaryKeys.onPaste = { [weak self] in
            self?.pasteText(UIPasteboard.general.string ?? "")
        }
        bar.secondaryKeys.onCopy = { [weak self] in
            self?.copyScreen()
        }
        inputBar = bar
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    deinit {
        displayLink?.invalidate()
    }

    func start() {
        let link = CADisplayLink(target: self, selector: #selector(tick))
        // Idle at a normal rate; touch scrolling raises the preferred rate to
        // the display's maximum (120 Hz on ProMotion) in beginScroll.
        link.preferredFrameRateRange = CAFrameRateRange(minimum: 30, maximum: 120, preferred: 60)
        link.add(to: .main, forMode: .common)
        displayLink = link
        lastTickTime = 0
    }

    private func setScrollFrameRate(_ rate: Float) {
        displayLink?.preferredFrameRateRange = CAFrameRateRange(minimum: 30, maximum: 120, preferred: rate)
    }

    func stop() {
        displayLink?.invalidate()
        displayLink = nil
    }

    private func computeCellSize() {
        let advance = ("M" as NSString).size(withAttributes: [.font: regularFont]).width
        let boldAdvance = ("M" as NSString).size(withAttributes: [.font: boldFont]).width
        cellWidth = max(1, advance.rounded())
        regularKern = cellWidth - advance
        boldKern = cellWidth - boldAdvance
        cellHeight = ceil(regularFont.lineHeight)
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        guard bounds.width > 0, bounds.height > 0 else { return }
        let cols = max(1, Int(bounds.width / cellWidth))
        let rows = max(1, Int(bounds.height / cellHeight))
        guard cols != currentCols || rows != currentRows else { return }
        currentCols = cols
        currentRows = rows
        bridge?.resize(sessionID: sessionID, cols: cols, rows: rows)
        bottomButton.frame = CGRect(x: bounds.width - 48, y: 8, width: 34, height: 34)
        copySelectionButton.frame = CGRect(x: bounds.width - 48, y: 50, width: 34, height: 34)
    }

    // MARK: Scrolling

    @objc private func handlePan(_ gesture: TerminalScrollPanGestureRecognizer) {
        switch gesture.state {
        case .began:
            #if DEBUG
            ScrollLatencyProbe.shared.panHandler(at: CACurrentMediaTime())
            #endif
            beginScroll()
            // The recognizer only fires after a couple of points of motion;
            // count that movement too so the content does not sit still until
            // the next touch event arrives.
            scrollDistance += gesture.translation(in: self).y
            gesture.setTranslation(.zero, in: self)
            clearSelection()
            // Paint from the touch handler instead of waiting for the next
            // display-link tick: the idle link runs at 60 Hz, and one frame of
            // delay is exactly what makes the start of a drag feel detached.
            commitScrollTarget()
            setNeedsDisplay()
        case .changed:
            scrollDistance += gesture.translation(in: self).y
            gesture.setTranslation(.zero, in: self)
            commitScrollTarget()
            setNeedsDisplay()
        case .ended:
            isPanning = false
            let velocity = gesture.velocity(in: self).y
            let maximum = CGFloat(max(0, snapshot.scrollback))
            if rawScrollOffset < 0 || rawScrollOffset > maximum {
                // Released while the content is past an end: spring back
                // instead of snapping or continuing to push into nothing.
                startBounceBack()
            } else if abs(velocity) >= 60 {
                scrollVelocity = min(8000, max(-8000, velocity))
            } else {
                settleScroll()
            }
        case .cancelled, .failed:
            isPanning = false
            settleOrBounce()
        default:
            break
        }
    }

    /// Ask the core for the newest whole-line offset without waiting for the
    /// next display-link tick. Repeated targets are dropped, so a drag sends
    /// at most one request per crossed line.
    private func commitScrollTarget() {
        guard scrollActive else { return }
        let target = settleTarget ?? targetScrollOffset
        guard target != requestedScrollOffset else { return }
        bridge?.scrollTo(sessionID: sessionID, offset: target)
        requestedScrollOffset = target
    }

    private func beginScroll() {
        // Freeze whatever is on screen as the new origin, even if the last
        // fling had not been fully acknowledged by the core yet.
        scrollBaseOffset = scrollActive ? rawScrollOffset : CGFloat(snapshot.viewOffset)
        scrollDistance = 0
        scrollVelocity = 0
        settleTarget = nil
        settleFrames = 0
        bounceBack = false
        bounceTargetDistance = 0
        requestedScrollOffset = snapshot.viewOffset
        scrollActive = true
        isPanning = true
        setScrollFrameRate(120)
    }

    /// Continuous view offset, before rubber-banding at the ends.
    private var rawScrollOffset: CGFloat {
        scrollBaseOffset + scrollDistance / cellHeight
    }

    /// UIScrollView-like resistance for movement past either end. The first
    /// points track the finger closely and the curve flattens out instead of
    /// stopping at a hard limit, so the bottom of the screen still follows an
    /// upward swipe.
    private func rubberBand(_ distance: CGFloat) -> CGFloat {
        let dimension = max(bounds.height, 1)
        let coefficient: CGFloat = 0.6
        return (distance * dimension * coefficient) / (dimension + coefficient * distance)
    }

    /// The continuous offset the screen should show. Past either end the
    /// movement is damped, like a scroll view's bounce.
    private var desiredScrollOffset: CGFloat {
        let maximum = CGFloat(max(0, snapshot.scrollback))
        let raw = rawScrollOffset
        if raw < 0 {
            return -rubberBand(-raw * cellHeight) / cellHeight
        }
        if raw > maximum {
            return maximum + rubberBand((raw - maximum) * cellHeight) / cellHeight
        }
        return raw
    }

    /// The whole-line offset to ask the core for. Flooring (rather than
    /// rounding) means the snapshot never jumps ahead of the finger; the
    /// leftover fraction stays in the drawn translation.
    private var targetScrollOffset: Int {
        let maximum = max(0, snapshot.scrollback)
        return min(max(Int(desiredScrollOffset.rounded(.down)), 0), maximum)
    }

    /// Fractional translation applied on top of the snapshot the core sent.
    private func visualScrollOffset() -> CGFloat {
        guard scrollActive else { return 0 }
        let offset = (desiredScrollOffset - CGFloat(snapshot.viewOffset)) * cellHeight
        // Past an end there is no core offset to catch up to: the movement is
        // the rubber band itself, so clamping it would make a long swipe stop
        // following the finger. Inside the range keep the clamp so a busy core
        // cannot push the whole screen away while targets are queued.
        let maximum = CGFloat(max(0, snapshot.scrollback))
        let raw = rawScrollOffset
        if raw < 0 || raw > maximum {
            return offset
        }
        let limit = cellHeight * 6
        return min(limit, max(-limit, offset))
    }

    /// Settle inside the scrollback, or spring back when the gesture ended
    /// past either end.
    private func settleOrBounce() {
        let maximum = CGFloat(max(0, snapshot.scrollback))
        let raw = rawScrollOffset
        if raw < 0 || raw > maximum {
            startBounceBack()
        } else {
            settleScroll()
        }
    }

    /// Animate the damped overscroll back to the nearest whole-line boundary.
    private func startBounceBack() {
        let maximum = CGFloat(max(0, snapshot.scrollback))
        let boundary = min(max(rawScrollOffset, 0), maximum)
        bounceTargetDistance = (boundary - scrollBaseOffset) * cellHeight
        scrollVelocity = 0
        settleTarget = nil
        settleFrames = 0
        isPanning = false
        if abs(scrollDistance - bounceTargetDistance) < 0.5 {
            scrollDistance = bounceTargetDistance
            bounceBack = false
            settleScroll()
            return
        }
        bounceBack = true
        scrollActive = true
        setScrollFrameRate(120)
    }

    private func settleScroll() {
        scrollVelocity = 0
        let maximum = max(0, snapshot.scrollback)
        let target = min(max(Int(desiredScrollOffset.rounded()), 0), maximum)
        scrollBaseOffset = CGFloat(target)
        scrollDistance = 0
        settleTarget = target
        settleFrames = 0
        requestedScrollOffset = snapshot.viewOffset
        if snapshot.viewOffset == target {
            finishScroll()
        }
    }

    private func finishScroll() {
        scrollActive = false
        isPanning = false
        scrollVelocity = 0
        settleTarget = nil
        settleFrames = 0
        bounceBack = false
        bounceTargetDistance = 0
        scrollDistance = 0
        scrollBaseOffset = 0
        setScrollFrameRate(60)
        if lastVisualOffset != 0 {
            lastVisualOffset = 0
            setNeedsDisplay()
        }
    }

    private func advanceMomentum(_ deltaTime: CFTimeInterval) {
        guard scrollActive, !isPanning else { return }
        if bounceBack {
            let remaining = bounceTargetDistance - scrollDistance
            if abs(remaining) < 0.5 {
                scrollDistance = bounceTargetDistance
                bounceBack = false
                settleScroll()
            } else {
                // Ease out: fast at first, then gently into the boundary.
                scrollDistance += remaining * min(1, CGFloat(deltaTime) * 18)
            }
            setNeedsDisplay()
            return
        }
        if scrollVelocity != 0 {
            scrollDistance += scrollVelocity * CGFloat(deltaTime)
            // Standard scroll-view-like decay: about a third of a second to
            // lose most of the speed.
            scrollVelocity *= CGFloat(exp(-deltaTime / 0.32))
            if abs(scrollVelocity) < 20 {
                settleOrBounce()
            }
            setNeedsDisplay()
        } else if settleTarget == nil {
            settleOrBounce()
        }
    }

    @objc private func jumpToBottom() {
        scrollVelocity = 0
        isPanning = false
        bounceBack = false
        bounceTargetDistance = 0
        scrollBaseOffset = 0
        scrollDistance = 0
        settleTarget = 0
        settleFrames = 0
        requestedScrollOffset = snapshot.viewOffset
        scrollActive = true
        bottomButton.isHidden = true
        if snapshot.viewOffset == 0 {
            finishScroll()
        }
    }

    // MARK: Selection

    @objc private func handleLongPress(_ gesture: UILongPressGestureRecognizer) {
        let point = gesture.location(in: self)
        switch gesture.state {
        case .began:
            guard let position = cellPosition(at: point) else { return }
            selectionAnchor = position
            selectionFocus = position
            setNeedsDisplay()
            updateOverlayButtons()
        case .changed:
            guard selectionAnchor != nil, let position = cellPosition(at: point) else { return }
            selectionFocus = position
            setNeedsDisplay()
        case .ended:
            guard selectionAnchor != nil else { return }
            updateOverlayButtons()
            UISelectionFeedbackGenerator().selectionChanged()
        case .cancelled, .failed:
            clearSelection()
        default:
            break
        }
    }

    /// The grid cell under a touch, clamped to the screen that is displayed.
    private func cellPosition(at point: CGPoint) -> CellPosition? {
        guard snapshot.cols > 0, !snapshot.cells.isEmpty else { return nil }
        let y = point.y - visualScrollOffset()
        let row = min(max(0, Int(y / cellHeight)), snapshot.cells.count - 1)
        let column = min(max(0, Int(point.x / cellWidth)), snapshot.cols - 1)
        return CellPosition(row: row, column: column)
    }

    private var selectionRange: (start: CellPosition, end: CellPosition)? {
        guard let anchor = selectionAnchor, let focus = selectionFocus else { return nil }
        if anchor.row < focus.row || (anchor.row == focus.row && anchor.column <= focus.column) {
            return (anchor, focus)
        }
        return (focus, anchor)
    }

    private func clearSelection() {
        guard selectionAnchor != nil || selectionFocus != nil else { return }
        selectionAnchor = nil
        selectionFocus = nil
        setNeedsDisplay()
        updateOverlayButtons()
    }

    private func updateOverlayButtons() {
        let selecting = selectionRange != nil
        copySelectionButton.isHidden = !selecting
        bottomButton.isHidden = selecting || snapshot.viewOffset == 0
    }

    @objc private func copySelectionTapped() {
        copySelection()
    }

    private func selectedText() -> String {
        guard let range = selectionRange else { return "" }
        var lines: [String] = []
        for row in range.start.row...range.end.row {
            guard row < snapshot.cells.count else { break }
            let cells = snapshot.cells[row]
            let first = row == range.start.row ? range.start.column : 0
            let last = row == range.end.row ? range.end.column : max(0, snapshot.cols - 1)
            var line = ""
            var column = first
            while column <= last {
                if column < cells.count {
                    let cell = cells[column]
                    if cell.flags & 2 == 0 { // a wide tail holds no glyph
                        if cell.rune != 0, let scalar = UnicodeScalar(cell.rune) {
                            line.append(Character(scalar))
                        } else {
                            line.append(" ")
                        }
                    }
                } else {
                    line.append(" ")
                }
                column += 1
            }
            while line.hasSuffix(" ") { line.removeLast() }
            lines.append(line)
        }
        return lines.joined(separator: "\n")
    }

    private func copySelection() {
        let text = selectedText()
        clearSelection()
        guard !text.isEmpty else { return }
        UIPasteboard.general.string = text
        UINotificationFeedbackGenerator().notificationOccurred(.success)
    }

    // MARK: Copy

    private func copyScreen() {
        if selectionRange != nil {
            copySelection()
            return
        }
        let text = visibleText()
        guard !text.isEmpty else { return }
        UIPasteboard.general.string = text
        UINotificationFeedbackGenerator().notificationOccurred(.success)
    }

    private func visibleText() -> String {
        snapshot.cells.map { row -> String in
            var line = ""
            for cell in row {
                if cell.flags & 2 != 0 { continue } // wide tail
                guard cell.rune != 0, let scalar = UnicodeScalar(cell.rune) else {
                    line.append(" ")
                    continue
                }
                line.append(Character(scalar))
            }
            return line.trimmingCharacters(in: .whitespaces)
        }
        .joined(separator: "\n")
        .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func pasteText(_ text: String) {
        guard !text.isEmpty else { return }
        if snapshot.bracketedPaste {
            send(Data(("\u{1B}[200~" + text + "\u{1B}[201~").utf8))
        } else {
            send(Data(text.utf8))
        }
    }

    // MARK: Snapshot

    @objc private func tick() {
        let now = CACurrentMediaTime()
        let deltaTime = lastTickTime == 0
            ? 1.0 / 60.0
            : min(max(now - lastTickTime, 0), 1.0 / 20.0)
        lastTickTime = now

        advanceMomentum(deltaTime)

        // Send at most one absolute target per frame, no matter how many
        // touch samples arrived. The serial core queue therefore never builds
        // a backlog of obsolete scroll positions.
        if scrollActive {
            commitScrollTarget()
        }

        var snapshotChanged = false
        let count = buffer.withUnsafeMutableBytes { raw in
            MottyScreenSnapshot(sessionID, lastVersion, raw.baseAddress, Int32(raw.count))
        }
        if count > 0 {
            let data = Data(buffer[0..<Int(count)])
            if let parsed = ScreenSnapshot(data: data) {
                snapshot = parsed
                lastVersion = parsed.version
                updateOverlayButtons()
                snapshotChanged = true
                #if DEBUG
                if parsed.viewOffset != 0 {
                    ScrollLatencyProbe.shared.coreAck(at: CACurrentMediaTime())
                }
                sendAutoTypeIfReady()
                #endif
            }
        } else if count < 0 {
            let needed = -Int(count)
            if needed > buffer.count, needed <= 16 * 1024 * 1024 {
                buffer = [UInt8](repeating: 0, count: needed)
            }
        }

        if let target = settleTarget {
            settleFrames += 1
            if snapshot.viewOffset == target || settleFrames > 30 {
                finishScroll()
                snapshotChanged = true
            }
        }

        let visual = visualScrollOffset()
        if snapshotChanged || abs(visual - lastVisualOffset) > 0.01 {
            lastVisualOffset = visual
            setNeedsDisplay()
        }
        #if DEBUG
        if scrollActive && isPanning {
            // How much of the finger's movement the 6-line clamp is hiding.
            let gap = desiredScrollOffset - CGFloat(snapshot.viewOffset)
            let lagLines = abs(gap * cellHeight - visual) / cellHeight
            ScrollLatencyProbe.shared.frame(
                visualOffset: visual,
                lagLines: lagLines,
                at: CACurrentMediaTime()
            )
        }
        #endif
    }

    #if DEBUG
    private var scrollProbeSummary: String {
        let probe = ScrollLatencyProbe.shared.summary
        let visual = String(format: "%.1f", lastVisualOffset)
        let phase = MottyBridge.shared.sessions.first { $0.id == sessionID }?.phase ?? "-"
        return probe
            + " sb=\(snapshot.scrollback)"
            + " off=\(snapshot.viewOffset)"
            + " vis=\(visual)"
            + " pan=\(isPanning ? 1 : 0)"
            + " active=\(scrollActive ? 1 : 0)"
            + " phase=\(phase)"
            + " rows=\(currentRows)"
            + " cols=\(currentCols)"
            + " line=" + lastCompletionLine.replacingOccurrences(of: " ", with: "_")
            + " " + (inputBar?.probeState ?? "")
    }

    /// Fill the scrollback once the shell has drawn its first prompt. Waiting
    /// for a non-empty screen means the command never lands in a password
    /// prompt or before the remote shell is ready.
    private func sendAutoTypeIfReady() {
        guard !autoTypeSent, let command = autoTypeCommand else { return }
        let hasText = snapshot.cells.contains { row in
            row.contains { $0.rune != 0 }
        }
        guard hasText else { return }
        autoTypeSent = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) { [weak self] in
            self?.send(Data((command + "\r").utf8))
        }
    }
    #endif

    // MARK: Drawing

    override func draw(_ rect: CGRect) {
        guard let context = UIGraphicsGetCurrentContext() else { return }
        context.setFillColor(terminalBackground.cgColor)
        context.fill(rect)
        context.textMatrix = .identity

        let snap = snapshot
        guard !snap.cells.isEmpty else { return }
        let offset = visualScrollOffset()
        #if DEBUG
        ScrollLatencyProbe.shared.draw(visualOffset: offset, at: CACurrentMediaTime())
        #endif
        for (rowIndex, row) in snap.cells.enumerated() {
            let y = pixelAligned(CGFloat(rowIndex) * cellHeight + offset)
            guard y + cellHeight >= rect.minY, y <= rect.maxY else { continue }
            drawBackgrounds(row, y: y, context: context)
            drawText(row, y: y)
        }
        drawSelection(offset: offset, context: context)
        drawCursor(snap, offset: offset, context: context)
    }

    private func pixelAligned(_ value: CGFloat) -> CGFloat {
        let scale = max(1, traitCollection.displayScale)
        return (value * scale).rounded() / scale
    }

    private func drawSelection(offset: CGFloat, context: CGContext) {
        guard let range = selectionRange else { return }
        context.setFillColor(UIColor.systemBlue.withAlphaComponent(0.3).cgColor)
        for row in range.start.row...range.end.row {
            guard row < snapshot.cells.count else { break }
            let first = row == range.start.row ? range.start.column : 0
            let last = row == range.end.row ? range.end.column : max(0, snapshot.cols - 1)
            guard last >= first else { continue }
            context.fill(CGRect(
                x: CGFloat(first) * cellWidth,
                y: pixelAligned(CGFloat(row) * cellHeight + offset),
                width: CGFloat(last - first + 1) * cellWidth,
                height: cellHeight
            ))
        }
    }

    private func backgroundColor(for cell: ScreenSnapshot.Cell) -> UIColor? {
        if cell.attr & TerminalAttr.inverse != 0 {
            return TerminalColor.kind(cell.fg) == .defaultValue
                ? terminalForeground
                : uiColor(cell.fg, fallback: terminalForeground)
        }
        guard TerminalColor.kind(cell.bg) != .defaultValue else { return nil }
        return uiColor(cell.bg, fallback: terminalBackground)
    }

    /// Fills runs of the same background colour in one operation. A syntax-
    /// highlighted screen used to issue a fill per cell; most rows now take
    /// one or two.
    private func drawBackgrounds(_ row: [ScreenSnapshot.Cell], y: CGFloat, context: CGContext) {
        var runStart = 0
        var runColor: UIColor?
        func flush(upTo end: Int) {
            guard let color = runColor, end > runStart else { return }
            context.setFillColor(color.cgColor)
            context.fill(CGRect(
                x: CGFloat(runStart) * cellWidth,
                y: y,
                width: CGFloat(end - runStart) * cellWidth,
                height: cellHeight
            ))
        }
        for (column, cell) in row.enumerated() {
            let color = backgroundColor(for: cell)
            if color != runColor {
                flush(upTo: column)
                runStart = column
                runColor = color
            }
        }
        flush(upTo: row.count)
    }

    private func drawText(_ row: [ScreenSnapshot.Cell], y: CGFloat) {
        var run = ""
        var runStart = 0
        var runStyle: TextStyle?

        func flush() {
            defer {
                run = ""
                runStyle = nil
            }
            guard let style = runStyle, !run.isEmpty else { return }
            let font = style.bold ? boldFont : regularFont
            var attributes = style.attributes(font: font, foreground: style.foreground)
            attributes[.kern] = style.bold ? boldKern : regularKern
            let point = CGPoint(x: CGFloat(runStart) * cellWidth,
                                y: y + (cellHeight - font.lineHeight) / 2)
            (run as NSString).draw(at: point, withAttributes: attributes)
        }

        for (column, cell) in row.enumerated() {
            if cell.flags & 2 != 0 { continue } // wide tail holds no glyph
            if cell.attr & TerminalAttr.hidden != 0 {
                flush()
                continue
            }
            guard let scalar = UnicodeScalar(cell.rune), cell.rune != 0 else {
                flush()
                continue
            }
            let style = TextStyle(cell: cell, foreground: terminalForeground, background: terminalBackground)
            let wide = cell.width == 2
            if wide || style != runStyle {
                flush()
            }
            if run.isEmpty {
                runStart = column
                runStyle = style
            }
            run.append(Character(scalar))
            if wide {
                flush()
            }
        }
        flush()
    }

    private struct TextStyle: Equatable {
        var foreground = UIColor.clear
        var bold = false
        var italic = false
        var underline = false
        var strike = false
        var dim = false

        init() {}

        init(cell: ScreenSnapshot.Cell, foreground: UIColor, background: UIColor) {
            let inverse = cell.attr & TerminalAttr.inverse != 0
            let fg = TextStyle.color(cell.fg, fallback: foreground)
            let bg = TextStyle.color(cell.bg, fallback: background)
            self.foreground = inverse ? bg : fg
            bold = cell.attr & TerminalAttr.bold != 0
            italic = cell.attr & TerminalAttr.italic != 0
            underline = cell.attr & TerminalAttr.underline != 0
            strike = cell.attr & TerminalAttr.strike != 0
            dim = cell.attr & TerminalAttr.dim != 0
        }

        func attributes(font: UIFont, foreground: UIColor) -> [NSAttributedString.Key: Any] {
            var attributes: [NSAttributedString.Key: Any] = [
                .font: font,
                .foregroundColor: dim ? foreground.withAlphaComponent(0.6) : foreground,
            ]
            if underline {
                attributes[.underlineStyle] = NSUnderlineStyle.single.rawValue
            }
            if strike {
                attributes[.strikethroughStyle] = NSUnderlineStyle.single.rawValue
            }
            if italic {
                attributes[.obliqueness] = 0.2
            }
            return attributes
        }

        static func color(_ raw: UInt32, fallback: UIColor) -> UIColor {
            switch TerminalColor.kind(raw) {
            case .defaultValue:
                return fallback
            case .palette:
                let index = min(TerminalColor.index(raw), TerminalColor.palette.count - 1)
                return color(TerminalColor.palette[index] | (2 << 24), fallback: fallback)
            case .trueColor:
                let (red, green, blue) = TerminalColor.rgb(raw)
                return UIColor(red: CGFloat(red) / 255, green: CGFloat(green) / 255,
                               blue: CGFloat(blue) / 255, alpha: 1)
            }
        }
    }

    private func uiColor(_ raw: UInt32, fallback: UIColor) -> UIColor {
        TextStyle.color(raw, fallback: fallback)
    }

    private func drawCursor(_ snap: ScreenSnapshot, offset: CGFloat, context: CGContext) {
        guard snap.cursorVisible, snap.cursorY < snap.cells.count else { return }
        let row = snap.cells[snap.cursorY]
        guard snap.cursorX < row.count else { return }
        let cell = row[snap.cursorX]
        if cell.flags & 2 != 0 { return }
        let x = CGFloat(snap.cursorX) * cellWidth
        let y = pixelAligned(CGFloat(snap.cursorY) * cellHeight + offset)
        let width = CGFloat(cell.width == 2 ? 2 : 1) * cellWidth
        context.setFillColor(terminalForeground.withAlphaComponent(0.55).cgColor)
        switch snap.cursorStyle {
        case 1: // underline
            context.fill(CGRect(x: x, y: y + cellHeight - 2, width: width, height: 2))
        case 2: // bar
            context.fill(CGRect(x: x, y: y, width: 2, height: cellHeight))
        default: // block
            context.fill(CGRect(x: x, y: y, width: width, height: cellHeight))
        }
    }

    // MARK: Input

    private func send(_ data: Data) {
        guard !data.isEmpty else { return }
        bridge?.input(sessionID: sessionID, data: data)
        scheduleCompletion()
    }

    // MARK: Inline completion

    /// Asks the core what the line could continue into, after the shell has had
    /// a moment to echo the keystroke.
    private func scheduleCompletion(after delay: TimeInterval = 0.22) {
        completionWork?.cancel()
        guard isFirstResponder else {
            showSuggestions([])
            return
        }
        let work = DispatchWorkItem { [weak self] in self?.requestCompletion() }
        completionWork = work
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: work)
    }

    private func requestCompletion() {
        guard let bridge, isFirstResponder else {
            showSuggestions([])
            return
        }
        completionSequence += 1
        let sequence = completionSequence
        let locale = AppLanguage.effectiveLocale.identifier
        bridge.complete(sessionID: sessionID, locale: locale) { [weak self] result in
            guard let self, sequence == self.completionSequence else { return }
            switch result {
            case .success(let answer):
                self.lastCompletionLine = answer.line
                // The core reads the line from the screen; an empty line means
                // this is not a plain prompt (a password, vim, a menu) and
                // nothing may be suggested.
                if answer.suppressed || answer.line.isEmpty {
                    self.showSuggestions([])
                } else {
                    self.showSuggestions(answer.candidates)
                }
            case .failure:
                self.showSuggestions([])
            }
        }
    }

    private func showSuggestions(_ candidates: [CompletionCandidate]) {
        inputBar?.show(candidates)
    }

    override func resignFirstResponder() -> Bool {
        let resigned = super.resignFirstResponder()
        if resigned {
            completionWork?.cancel()
            completionSequence += 1
            showSuggestions([])
        }
        return resigned
    }

    @objc private func focus() {
        if scrollActive && !isPanning {
            scrollVelocity = 0
            settleOrBounce()
        }
        if selectionRange != nil {
            clearSelection()
            return
        }
        becomeFirstResponder()
    }

    override var canBecomeFirstResponder: Bool { true }
    override var inputAccessoryView: UIView? { inputBar }

    var hasText: Bool { true }

    var autocorrectionType: UITextAutocorrectionType = .no
    var autocapitalizationType: UITextAutocapitalizationType = .none
    var spellCheckingType: UITextSpellCheckingType = .no
    var keyboardType: UIKeyboardType = .asciiCapable

    func insertText(_ text: String) {
        guard let data = text.data(using: .utf8) else { return }
        send(data)
    }

    func deleteBackward() {
        send(Data([0x7F]))
    }

    override func paste(_ sender: Any?) {
        pasteText(UIPasteboard.general.string ?? "")
    }

    override var keyCommands: [UIKeyCommand]? {
        var commands = [
            UIKeyCommand(input: UIKeyCommand.inputUpArrow, modifierFlags: [], action: #selector(keyUp)),
            UIKeyCommand(input: UIKeyCommand.inputDownArrow, modifierFlags: [], action: #selector(keyDown)),
            UIKeyCommand(input: UIKeyCommand.inputRightArrow, modifierFlags: [], action: #selector(keyRight)),
            UIKeyCommand(input: UIKeyCommand.inputLeftArrow, modifierFlags: [], action: #selector(keyLeft)),
        ]
        // A hardware keyboard can send the control characters a terminal needs.
        for letter in "abcdefghijklmnopqrstuvwxyz" {
            commands.append(UIKeyCommand(
                input: String(letter),
                modifierFlags: .control,
                action: #selector(controlKey(_:))
            ))
        }
        commands.append(UIKeyCommand(input: "[", modifierFlags: .control, action: #selector(controlBracket)))
        commands.append(UIKeyCommand(input: "\\", modifierFlags: .control, action: #selector(controlBackslash)))
        commands.append(UIKeyCommand(input: "c", modifierFlags: .command, action: #selector(copyCommand)))
        return commands
    }

    /// A hardware keyboard copies the selection, or the screen when there is
    /// none, exactly like the key bar's Copy key.
    @objc private func copyCommand() {
        copyScreen()
    }

    @objc private func keyUp() { sendKey("A") }
    @objc private func keyDown() { sendKey("B") }
    @objc private func keyRight() { sendKey("C") }
    @objc private func keyLeft() { sendKey("D") }

    @objc private func controlKey(_ command: UIKeyCommand) {
        guard let scalar = command.input?.unicodeScalars.first else { return }
        let value = scalar.value
        if value >= 97 && value <= 122 {
            send(Data([UInt8(value - 96)]))
        }
    }

    @objc private func controlBracket() { send(Data([0x1B])) }
    @objc private func controlBackslash() { send(Data([0x1C])) }

    private func sendKey(_ suffix: String) {
        let prefix = snapshot.appCursorKeys ? "\u{1B}O" : "\u{1B}["
        send(Data((prefix + suffix).utf8))
    }
}

/// A one-finger pan that starts after about three points instead of the
/// roughly ten points `UIPanGestureRecognizer` waits for. That touch slop is
/// useful for buttons, but at the start of a terminal drag it reads as the
/// text lagging behind the finger. The recognizer exposes the same
/// translation/velocity calls the scroll handler already used.
final class TerminalScrollPanGestureRecognizer: UIGestureRecognizer {
    /// One point of finger travel is enough to start scrolling. At the slow
    /// beginning of a deliberate swipe, three points can take ~100 ms, which
    /// reads as the terminal ignoring the finger.
    private let startThreshold: CGFloat = 1
    private var startPoint: CGPoint = .zero
    private var currentPoint: CGPoint = .zero
    private var lastPoint: CGPoint = .zero
    private var lastTimestamp: CFTimeInterval = 0
    private var translationOffset: CGPoint = .zero
    private var currentVelocity: CGPoint = .zero

    override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesBegan(touches, with: event)
        guard numberOfTouches == 1, let touch = touches.first else {
            state = .failed
            return
        }
        startPoint = touch.location(in: view)
        currentPoint = startPoint
        lastPoint = startPoint
        lastTimestamp = touch.timestamp
        translationOffset = .zero
        currentVelocity = .zero
        #if DEBUG
        ScrollLatencyProbe.shared.touchDown(at: touch.timestamp)
        ScrollLatencyProbe.shared.touchDelivered(at: CACurrentMediaTime())
        #endif
    }

    override func touchesMoved(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesMoved(touches, with: event)
        guard numberOfTouches == 1, let touch = touches.first else {
            state = .failed
            return
        }
        guard state == .possible || state == .began || state == .changed else { return }
        let point = touch.location(in: view)
        #if DEBUG
        ScrollLatencyProbe.shared.touchMoved(
            eventTimestamp: touch.timestamp,
            at: CACurrentMediaTime()
        )
        #endif
        let elapsed = touch.timestamp - lastTimestamp
        if elapsed > 0 {
            currentVelocity = CGPoint(
                x: (point.x - lastPoint.x) / elapsed,
                y: (point.y - lastPoint.y) / elapsed
            )
        }
        currentPoint = point
        lastPoint = point
        lastTimestamp = touch.timestamp

        if state == .possible {
            let dx = point.x - startPoint.x
            let dy = point.y - startPoint.y
            let distance = hypot(dx, dy)
            guard distance >= startThreshold else { return }
            #if DEBUG
            ScrollLatencyProbe.shared.panBegan(at: CACurrentMediaTime(), distance: distance)
            #endif
            state = .began
        } else {
            state = .changed
        }
    }

    override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesEnded(touches, with: event)
        if state == .began || state == .changed {
            state = .ended
        } else {
            state = .failed
        }
    }

    override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent) {
        super.touchesCancelled(touches, with: event)
        if state == .began || state == .changed {
            state = .cancelled
        } else {
            state = .failed
        }
    }

    override func reset() {
        super.reset()
        startPoint = .zero
        currentPoint = .zero
        lastPoint = .zero
        lastTimestamp = 0
        translationOffset = .zero
        currentVelocity = .zero
    }

    func translation(in _: UIView?) -> CGPoint {
        CGPoint(
            x: currentPoint.x - startPoint.x - translationOffset.x,
            y: currentPoint.y - startPoint.y - translationOffset.y
        )
    }

    func setTranslation(_ translation: CGPoint, in _: UIView?) {
        translationOffset = CGPoint(
            x: currentPoint.x - startPoint.x - translation.x,
            y: currentPoint.y - startPoint.y - translation.y
        )
    }

    func velocity(in _: UIView?) -> CGPoint {
        currentVelocity
    }
}
