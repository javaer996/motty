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
    private var keyBar: KeyBarView?
    private var panRemainder: CGFloat = 0

    private struct CellPosition {
        var row: Int
        var column: Int
    }

    /// A text selection made with a long press, in view cells. The anchor is
    /// where the press began; the focus follows the finger.
    private var selectionAnchor: CellPosition?
    private var selectionFocus: CellPosition?

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
        let pan = UIPanGestureRecognizer(target: self, action: #selector(handlePan(_:)))
        pan.maximumNumberOfTouches = 1
        addGestureRecognizer(pan)
        let longPress = UILongPressGestureRecognizer(target: self, action: #selector(handleLongPress(_:)))
        longPress.minimumPressDuration = 0.35
        longPress.cancelsTouchesInView = true
        addGestureRecognizer(longPress)
        // A drag scrolls unless it started as a long press, which extends the
        // selection instead.
        pan.require(toFail: longPress)

        addSubview(bottomButton)
        addSubview(copySelectionButton)

        keyBar = KeyBarView()
        keyBar?.onKey = { [weak self] data in
            self?.send(data)
        }
        keyBar?.onPaste = { [weak self] in
            self?.pasteText(UIPasteboard.general.string ?? "")
        }
        keyBar?.onCopy = { [weak self] in
            self?.copyScreen()
        }
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    deinit {
        displayLink?.invalidate()
    }

    func start() {
        let link = CADisplayLink(target: self, selector: #selector(tick))
        link.preferredFramesPerSecond = 30
        link.add(to: .main, forMode: .common)
        displayLink = link
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

    @objc private func handlePan(_ gesture: UIPanGestureRecognizer) {
        switch gesture.state {
        case .began:
            panRemainder = 0
            clearSelection()
        case .changed:
            panRemainder += gesture.translation(in: self).y
            gesture.setTranslation(.zero, in: self)
            let lines = Int(panRemainder / cellHeight)
            guard lines != 0 else { return }
            panRemainder -= CGFloat(lines) * cellHeight
            // Dragging down reveals earlier output.
            bridge?.scroll(sessionID: sessionID, lines: lines)
        default:
            panRemainder = 0
        }
    }

    @objc private func jumpToBottom() {
        bridge?.scroll(sessionID: sessionID, lines: -snapshot.viewOffset)
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
        let row = min(max(0, Int(point.y / cellHeight)), snapshot.cells.count - 1)
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
        let count = buffer.withUnsafeMutableBytes { raw in
            MottyScreenSnapshot(sessionID, lastVersion, raw.baseAddress, Int32(raw.count))
        }
        if count > 0 {
            let data = Data(buffer[0..<Int(count)])
            guard let parsed = ScreenSnapshot(data: data) else { return }
            snapshot = parsed
            lastVersion = parsed.version
            updateOverlayButtons()
            setNeedsDisplay()
        } else if count < 0 {
            let needed = -Int(count)
            if needed > buffer.count, needed <= 16 * 1024 * 1024 {
                buffer = [UInt8](repeating: 0, count: needed)
            }
        }
    }

    // MARK: Drawing

    override func draw(_ rect: CGRect) {
        guard let context = UIGraphicsGetCurrentContext() else { return }
        context.setFillColor(terminalBackground.cgColor)
        context.fill(rect)
        context.textMatrix = .identity

        let snap = snapshot
        guard !snap.cells.isEmpty else { return }
        for (rowIndex, row) in snap.cells.enumerated() {
            let y = CGFloat(rowIndex) * cellHeight
            guard y < bounds.height else { break }
            drawBackgrounds(row, y: y, context: context)
            drawText(row, y: y)
        }
        drawSelection(context: context)
        drawCursor(snap, context: context)
    }

    private func drawSelection(context: CGContext) {
        guard let range = selectionRange else { return }
        context.setFillColor(UIColor.systemBlue.withAlphaComponent(0.3).cgColor)
        for row in range.start.row...range.end.row {
            guard row < snapshot.cells.count else { break }
            let first = row == range.start.row ? range.start.column : 0
            let last = row == range.end.row ? range.end.column : max(0, snapshot.cols - 1)
            guard last >= first else { continue }
            context.fill(CGRect(
                x: CGFloat(first) * cellWidth,
                y: CGFloat(row) * cellHeight,
                width: CGFloat(last - first + 1) * cellWidth,
                height: cellHeight
            ))
        }
    }

    private func drawBackgrounds(_ row: [ScreenSnapshot.Cell], y: CGFloat, context: CGContext) {
        for (column, cell) in row.enumerated() {
            let inverse = cell.attr & TerminalAttr.inverse != 0
            let color: UIColor
            if inverse {
                color = TerminalColor.kind(cell.fg) == .defaultValue
                    ? terminalForeground
                    : uiColor(cell.fg, fallback: terminalForeground)
            } else {
                guard TerminalColor.kind(cell.bg) != .defaultValue else { continue }
                color = uiColor(cell.bg, fallback: terminalBackground)
            }
            context.setFillColor(color.cgColor)
            let width = CGFloat(cell.width == 2 ? 2 : 1) * cellWidth
            context.fill(CGRect(x: CGFloat(column) * cellWidth, y: y, width: width, height: cellHeight))
        }
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

    private func drawCursor(_ snap: ScreenSnapshot, context: CGContext) {
        guard snap.cursorVisible, snap.cursorY < snap.cells.count else { return }
        let row = snap.cells[snap.cursorY]
        guard snap.cursorX < row.count else { return }
        let cell = row[snap.cursorX]
        if cell.flags & 2 != 0 { return }
        let x = CGFloat(snap.cursorX) * cellWidth
        let y = CGFloat(snap.cursorY) * cellHeight
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
    }

    @objc private func focus() {
        if selectionRange != nil {
            clearSelection()
            return
        }
        becomeFirstResponder()
    }

    override var canBecomeFirstResponder: Bool { true }
    override var inputAccessoryView: UIView? { keyBar }

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
