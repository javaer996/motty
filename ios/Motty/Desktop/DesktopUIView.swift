import SwiftUI
import UIKit

/// Lets the SwiftUI chrome reach into the UIKit surface (to show or hide the
/// software keyboard, for example) without owning it.
final class DesktopSurfaceHandle {
    weak var view: DesktopSurfaceView?

    func toggleKeyboard() {
        view?.toggleSoftwareKeyboard()
    }

    func dismissKeyboard() {
        view?.hideSoftwareKeyboard()
    }
}

/// The remote picture and the gestures that drive it. Frames are pulled from
/// the Go core on the display link; the core keeps the full picture and hands
/// back only changed rectangles, so an idle desktop costs nothing.
struct DesktopSurface: UIViewRepresentable {
    let desktopID: UInt32
    let handle: DesktopSurfaceHandle
    let bridge: MottyBridge

    func makeUIView(context: Context) -> DesktopSurfaceView {
        let view = DesktopSurfaceView()
        view.desktopID = desktopID
        view.onPointer = { [weak view, weak bridge] x, y, buttons in
            guard let id = view?.desktopID else { return }
            bridge?.desktopPointer(desktopID: id, x: x, y: y, buttons: buttons)
        }
        view.onWheel = { [weak view, weak bridge] x, y, dx, dy in
            guard let id = view?.desktopID else { return }
            bridge?.desktopWheel(desktopID: id, x: x, y: y, dx: dx, dy: dy)
        }
        view.onKey = { [weak view, weak bridge] key, rune, typed, down, isRepeat in
            guard let id = view?.desktopID else { return }
            bridge?.desktopKey(desktopID: id, key: key, rune: rune, typed: typed, down: down, repeat: isRepeat)
        }
        view.onText = { [weak view, weak bridge] text in
            guard let id = view?.desktopID else { return }
            bridge?.desktopText(desktopID: id, text: text)
        }
        handle.view = view
        return view
    }

    func updateUIView(_ view: DesktopSurfaceView, context: Context) {
        if view.desktopID != desktopID {
            view.desktopID = desktopID
        }
        handle.view = view
    }
}

/// One remote desktop surface: draws BGRA frames, translates touches and keys,
/// and talks to the core through closures.
final class DesktopSurfaceView: UIView, UIKeyInput {
    var desktopID: UInt32 = 0 {
        didSet {
            guard oldValue != desktopID else { return }
            resetPicture()
        }
    }

    var onPointer: ((Int, Int, UInt8) -> Void)?
    var onWheel: ((Int, Int, Int, Int) -> Void)?
    var onKey: ((String?, String?, String?, Bool, Bool) -> Void)?
    var onText: ((String) -> Void)?

    // MARK: Picture state

    private var pixels = Data()
    private var pictureWidth = 0
    private var pictureHeight = 0
    private var frameVersion: UInt64 = 0
    private var frameBuffer = [UInt8](repeating: 0, count: 256 * 1024)
    private var displayLink: CADisplayLink?
    private var lastPoll: CFTimeInterval = 0
    private var zoom: CGFloat = 1

    // MARK: Input state

    private var softwareKeyboard = false
    private var heldButtons: UInt8 = 0
    private var scrollRemainder = CGPoint.zero
    private var downKeys = Set<UIKeyboardHIDUsage>()
    /// Modifier keys this view has told the remote are down but not released.
    private var heldModifiers: UIKeyModifierFlags = []

    private static let emptyInput = UIView(frame: .zero)

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .black
        isMultipleTouchEnabled = true
        isOpaque = true
        contentMode = .redraw
        installGestures()
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    override var canBecomeFirstResponder: Bool { true }
    override var inputView: UIView? {
        softwareKeyboard ? nil : Self.emptyInput
    }

    // MARK: Software keyboard

    func toggleSoftwareKeyboard() {
        softwareKeyboard.toggle()
        if softwareKeyboard {
            becomeFirstResponder()
            reloadInputViews()
        } else {
            reloadInputViews()
        }
    }

    func hideSoftwareKeyboard() {
        guard softwareKeyboard else { return }
        softwareKeyboard = false
        reloadInputViews()
    }

    // MARK: Lifecycle

    override func didMoveToWindow() {
        super.didMoveToWindow()
        if window != nil {
            startDisplayLink()
            // First responder so a hardware keyboard works without the user
            // tapping anything; the empty input view keeps the software one away.
            DispatchQueue.main.async { [weak self] in
                self?.becomeFirstResponder()
            }
        } else {
            stop()
        }
    }

    func stop() {
        displayLink?.invalidate()
        displayLink = nil
        releaseHeldModifiers()
        resignFirstResponder()
    }

    private func startDisplayLink() {
        guard displayLink == nil else { return }
        let link = CADisplayLink(target: self, selector: #selector(tick))
        link.preferredFrameRateRange = CAFrameRateRange(minimum: 20, maximum: 60, preferred: 60)
        link.add(to: .main, forMode: .common)
        displayLink = link
    }

    @objc private func tick(_ link: CADisplayLink) {
        // 30 Hz is plenty for a desktop; the core coalesces the rest.
        guard link.timestamp - lastPoll >= 1.0 / 30.0 else { return }
        lastPoll = link.timestamp
        pollFrame()
    }

    private func resetPicture() {
        frameVersion = 0
        pixels = Data()
        pictureWidth = 0
        pictureHeight = 0
        zoom = 1
        setNeedsDisplay()
    }

    private func pollFrame() {
        guard desktopID != 0 else { return }
        let result = frameBuffer.withUnsafeMutableBytes { raw -> Int32 in
            MottyDesktopFrame(desktopID, frameVersion, raw.baseAddress, Int32(raw.count))
        }
        if result > 0 {
            applyFrame(frameBuffer, count: Int(result))
        } else if result < -1 {
            // The core names the buffer size it needs.
            frameBuffer = [UInt8](repeating: 0, count: -Int(result))
            pollFrame()
        } else if result == -1 {
            // The desktop is gone; stop asking.
            displayLink?.invalidate()
            displayLink = nil
        }
    }

    private func applyFrame(_ bytes: [UInt8], count: Int) {
        guard count >= 24 else { return }
        bytes.withUnsafeBytes { raw in
            let version = raw.loadUnaligned(fromByteOffset: 0, as: UInt64.self)
            let width = Int(raw.loadUnaligned(fromByteOffset: 8, as: Int32.self))
            let height = Int(raw.loadUnaligned(fromByteOffset: 12, as: Int32.self))
            let rectCount = Int(raw.loadUnaligned(fromByteOffset: 16, as: UInt32.self))
            guard width > 0, height > 0, rectCount >= 0,
                  width <= 2732, height <= 2048, rectCount <= 4096 else { return }
            var offset = 24
            guard offset + rectCount * 16 <= count else { return }
            var rects: [(x: Int, y: Int, width: Int, height: Int)] = []
            rects.reserveCapacity(rectCount)
            for _ in 0..<rectCount {
                let x = Int(raw.loadUnaligned(fromByteOffset: offset, as: Int32.self))
                let y = Int(raw.loadUnaligned(fromByteOffset: offset + 4, as: Int32.self))
                let w = Int(raw.loadUnaligned(fromByteOffset: offset + 8, as: Int32.self))
                let h = Int(raw.loadUnaligned(fromByteOffset: offset + 12, as: Int32.self))
                offset += 16
                guard x >= 0, y >= 0, w > 0, h > 0, x + w <= width, y + h <= height else { return }
                rects.append((x, y, w, h))
            }
            if pictureWidth != width || pictureHeight != height || pixels.count != width * height * 4 {
                pictureWidth = width
                pictureHeight = height
                pixels = Data(count: width * height * 4)
            }
            var source = offset
            pixels.withUnsafeMutableBytes { destination in
                guard let destinationBase = destination.baseAddress, let sourceBase = raw.baseAddress else {
                    return
                }
                for rect in rects {
                    let rowBytes = rect.width * 4
                    for row in 0..<rect.height {
                        guard source + rowBytes <= count else { return }
                        let destinationRow = destinationBase.advanced(by: ((rect.y + row) * width + rect.x) * 4)
                        memcpy(destinationRow, sourceBase.advanced(by: source), rowBytes)
                        source += rowBytes
                    }
                }
            }
            frameVersion = version
        }
        setNeedsDisplay()
    }

    // MARK: Drawing

    private var pictureRect: CGRect {
        guard pictureWidth > 0, pictureHeight > 0 else { return .zero }
        let source = CGSize(width: pictureWidth, height: pictureHeight)
        let fitted = Self.aspectFit(source, in: bounds)
        guard zoom > 1 else { return fitted }
        let size = CGSize(width: fitted.width * zoom, height: fitted.height * zoom)
        return CGRect(
            x: bounds.midX - size.width / 2,
            y: bounds.midY - size.height / 2,
            width: size.width,
            height: size.height
        )
    }

    private static func aspectFit(_ size: CGSize, in bounds: CGRect) -> CGRect {
        guard size.width > 0, size.height > 0, bounds.width > 0, bounds.height > 0 else { return .zero }
        let scale = min(bounds.width / size.width, bounds.height / size.height)
        let width = size.width * scale
        let height = size.height * scale
        return CGRect(
            x: bounds.midX - width / 2,
            y: bounds.midY - height / 2,
            width: width,
            height: height
        )
    }

    private func makeImage() -> CGImage? {
        guard pictureWidth > 0, pictureHeight > 0, pixels.count == pictureWidth * pictureHeight * 4 else {
            return nil
        }
        guard let provider = CGDataProvider(data: pixels as CFData) else { return nil }
        let info = CGBitmapInfo(rawValue: CGImageAlphaInfo.noneSkipFirst.rawValue)
            .union(.byteOrder32Little)
        return CGImage(
            width: pictureWidth,
            height: pictureHeight,
            bitsPerComponent: 8,
            bitsPerPixel: 32,
            bytesPerRow: pictureWidth * 4,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: info,
            provider: provider,
            decode: nil,
            shouldInterpolate: false,
            intent: .defaultIntent
        )
    }

    override func draw(_ rect: CGRect) {
        guard let context = UIGraphicsGetCurrentContext() else { return }
        UIColor.black.setFill()
        context.fill(bounds)
        guard let image = makeImage() else { return }
        context.interpolationQuality = .none
        context.draw(image, in: pictureRect)
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        setNeedsDisplay()
    }

    // MARK: Coordinates

    private func pixelPoint(_ point: CGPoint) -> (Int, Int)? {
        let rect = pictureRect
        guard rect.width > 0, rect.height > 0, rect.contains(point) else { return nil }
        let x = Int(((point.x - rect.minX) / rect.width) * CGFloat(pictureWidth))
        let y = Int(((point.y - rect.minY) / rect.height) * CGFloat(pictureHeight))
        return (min(max(x, 0), pictureWidth - 1), min(max(y, 0), pictureHeight - 1))
    }

    private func movePointer(to point: CGPoint, buttons: UInt8) {
        guard let (x, y) = pixelPoint(point) else { return }
        onPointer?(x, y, buttons)
    }

    private func click(at point: CGPoint, button: UInt8) {
        guard let (x, y) = pixelPoint(point) else { return }
        onPointer?(x, y, button)
        onPointer?(x, y, 0)
    }

    // MARK: Gestures

    private func installGestures() {
        let tap = UITapGestureRecognizer(target: self, action: #selector(handleTap(_:)))
        addGestureRecognizer(tap)

        let doubleTap = UITapGestureRecognizer(target: self, action: #selector(handleDoubleTap(_:)))
        doubleTap.numberOfTapsRequired = 2
        addGestureRecognizer(doubleTap)
        tap.require(toFail: doubleTap)

        let longPress = UILongPressGestureRecognizer(target: self, action: #selector(handleLongPress(_:)))
        longPress.minimumPressDuration = 0.55
        longPress.allowableMovement = 8
        addGestureRecognizer(longPress)

        let drag = UIPanGestureRecognizer(target: self, action: #selector(handleDrag(_:)))
        drag.maximumNumberOfTouches = 1
        drag.require(toFail: longPress)
        addGestureRecognizer(drag)

        let rightTap = UITapGestureRecognizer(target: self, action: #selector(handleRightTap(_:)))
        rightTap.numberOfTouchesRequired = 2
        addGestureRecognizer(rightTap)

        let scroll = UIPanGestureRecognizer(target: self, action: #selector(handleScroll(_:)))
        scroll.minimumNumberOfTouches = 2
        scroll.maximumNumberOfTouches = 2
        addGestureRecognizer(scroll)

        let pinch = UIPinchGestureRecognizer(target: self, action: #selector(handlePinch(_:)))
        addGestureRecognizer(pinch)

        let hover = UIHoverGestureRecognizer(target: self, action: #selector(handleHover(_:)))
        addGestureRecognizer(hover)
    }

    @objc private func handleTap(_ gesture: UITapGestureRecognizer) {
        click(at: gesture.location(in: self), button: 1)
    }

    @objc private func handleDoubleTap(_ gesture: UITapGestureRecognizer) {
        click(at: gesture.location(in: self), button: 1)
        click(at: gesture.location(in: self), button: 1)
    }

    @objc private func handleRightTap(_ gesture: UITapGestureRecognizer) {
        click(at: gesture.location(in: self), button: 4)
    }

    @objc private func handleLongPress(_ gesture: UILongPressGestureRecognizer) {
        let point = gesture.location(in: self)
        switch gesture.state {
        case .began:
            guard let (x, y) = pixelPoint(point) else { return }
            heldButtons = 4
            onPointer?(x, y, heldButtons)
        case .ended, .cancelled, .failed:
            guard heldButtons != 0, let (x, y) = pixelPoint(point) else {
                heldButtons = 0
                return
            }
            onPointer?(x, y, 0)
            heldButtons = 0
        default:
            if heldButtons != 0 {
                movePointer(to: point, buttons: heldButtons)
            }
        }
    }

    @objc private func handleDrag(_ gesture: UIPanGestureRecognizer) {
        movePointer(to: gesture.location(in: self), buttons: heldButtons)
    }

    @objc private func handleScroll(_ gesture: UIPanGestureRecognizer) {
        let translation = gesture.translation(in: self)
        scrollRemainder.x += translation.x
        scrollRemainder.y += translation.y
        gesture.setTranslation(.zero, in: self)
        let notch: CGFloat = 40
        let dx = Int(scrollRemainder.x / notch)
        let dy = Int(-scrollRemainder.y / notch)
        guard dx != 0 || dy != 0 else { return }
        scrollRemainder.x -= CGFloat(dx) * notch
        scrollRemainder.y += CGFloat(dy) * notch
        guard let (x, y) = pixelPoint(gesture.location(in: self)) else { return }
        onWheel?(x, y, dx, dy)
    }

    @objc private func handlePinch(_ gesture: UIPinchGestureRecognizer) {
        switch gesture.state {
        case .changed:
            zoom = min(max(zoom * gesture.scale, 1), 4)
            gesture.scale = 1
            setNeedsDisplay()
        default:
            break
        }
    }

    @objc private func handleHover(_ gesture: UIHoverGestureRecognizer) {
        guard gesture.state == .changed else { return }
        movePointer(to: gesture.location(in: self), buttons: heldButtons)
    }

    // MARK: Hardware keyboard

    override func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        var handled = false
        for press in presses {
            guard let key = press.key else { continue }
            if handleModifier(key, down: true) {
                handled = true
                continue
            }
            sendModifierTransitions(for: key.modifierFlags)
            let repeatPress = downKeys.contains(key.keyCode)
            downKeys.insert(key.keyCode)
            send(key: key, down: true, isRepeat: repeatPress)
            handled = true
        }
        if !handled {
            super.pressesBegan(presses, with: event)
        }
    }

    override func pressesEnded(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        var handled = false
        for press in presses {
            guard let key = press.key else { continue }
            if handleModifier(key, down: false) {
                handled = true
                continue
            }
            downKeys.remove(key.keyCode)
            send(key: key, down: false, isRepeat: false)
            handled = true
        }
        if !handled {
            super.pressesEnded(presses, with: event)
        }
    }

    override func pressesCancelled(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        for press in presses {
            if let key = press.key {
                downKeys.remove(key.keyCode)
                send(key: key, down: false, isRepeat: false)
            }
        }
    }

    private func modifier(for code: UIKeyboardHIDUsage) -> (name: String, flag: UIKeyModifierFlags)? {
        switch code {
        case .keyboardLeftShift, .keyboardRightShift: return ("shift", .shift)
        case .keyboardLeftControl, .keyboardRightControl: return ("control", .control)
        case .keyboardLeftAlt, .keyboardRightAlt: return ("alt", .alternate)
        case .keyboardLeftGUI, .keyboardRightGUI: return ("meta", .command)
        default: return nil
        }
    }

    private func handleModifier(_ key: UIKey, down: Bool) -> Bool {
        guard let modifier = modifier(for: key.keyCode) else { return false }
        if down {
            heldModifiers.insert(modifier.flag)
        } else {
            heldModifiers.remove(modifier.flag)
        }
        onKey?(modifier.name, nil, nil, down, false)
        return true
    }

    /// Sends modifier presses the remote does not know about yet. iOS reports
    /// modifier flags on every key, but some keyboards never deliver the
    /// modifier keys themselves; without this Ctrl+C would arrive as a plain c.
    private func sendModifierTransitions(for flags: UIKeyModifierFlags) {
        let wanted: [(UIKeyModifierFlags, String)] = [
            (.shift, "shift"),
            (.control, "control"),
            (.alternate, "alt"),
            (.command, "meta"),
        ]
        for (flag, name) in wanted where flags.contains(flag) && !heldModifiers.contains(flag) {
            heldModifiers.insert(flag)
            onKey?(name, nil, nil, true, false)
        }
    }

    private func releaseHeldModifiers() {
        let wanted: [(UIKeyModifierFlags, String)] = [
            (.shift, "shift"),
            (.control, "control"),
            (.alternate, "alt"),
            (.command, "meta"),
        ]
        for (flag, name) in wanted where heldModifiers.contains(flag) {
            onKey?(name, nil, nil, false, false)
        }
        heldModifiers = []
    }

    private func send(key: UIKey, down: Bool, isRepeat: Bool) {
        if let name = Self.keyName(for: key.keyCode) {
            onKey?(name, nil, nil, down, isRepeat)
            return
        }
        let typed = key.characters
        let rune = key.charactersIgnoringModifiers
        guard !rune.isEmpty || !typed.isEmpty else { return }
        let runeText = rune.isEmpty ? nil : String(rune.prefix(1))
        let typedText = typed.isEmpty ? nil : String(typed.prefix(1))
        if let runeText {
            onKey?(nil, runeText, typedText, down, isRepeat)
        } else if let typedText {
            onKey?(nil, typedText, typedText, down, isRepeat)
        }
    }

    private static func keyName(for code: UIKeyboardHIDUsage) -> String? {
        switch code {
        case .keyboardReturnOrEnter: return "enter"
        case .keyboardTab: return "tab"
        case .keyboardDeleteOrBackspace: return "backspace"
        case .keyboardEscape: return "escape"
        case .keyboardDeleteForward: return "delete"
        case .keyboardInsert: return "insert"
        case .keyboardHome: return "home"
        case .keyboardEnd: return "end"
        case .keyboardPageUp: return "pageup"
        case .keyboardPageDown: return "pagedown"
        case .keyboardLeftArrow: return "left"
        case .keyboardRightArrow: return "right"
        case .keyboardUpArrow: return "up"
        case .keyboardDownArrow: return "down"
        case .keyboardCapsLock: return "capslock"
        case .keyboardApplication: return "menu"
        case .keyboardF1: return "f1"
        case .keyboardF2: return "f2"
        case .keyboardF3: return "f3"
        case .keyboardF4: return "f4"
        case .keyboardF5: return "f5"
        case .keyboardF6: return "f6"
        case .keyboardF7: return "f7"
        case .keyboardF8: return "f8"
        case .keyboardF9: return "f9"
        case .keyboardF10: return "f10"
        case .keyboardF11: return "f11"
        case .keyboardF12: return "f12"
        default: return nil
        }
    }

    // MARK: UIKeyInput (software keyboard)

    /// The remote owns whatever text exists there; reporting true keeps the
    /// software keyboard's delete key enabled.
    var hasText: Bool { true }

    func insertText(_ text: String) {
        guard !text.isEmpty else { return }
        if text == "\n" {
            onKey?("enter", nil, nil, true, false)
            onKey?("enter", nil, nil, false, false)
            return
        }
        onText?(text)
    }

    func deleteBackward() {
        onKey?("backspace", nil, nil, true, false)
        onKey?("backspace", nil, nil, false, false)
    }

    // No selection or caret: the remote owns the text.
    override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        false
    }
}
