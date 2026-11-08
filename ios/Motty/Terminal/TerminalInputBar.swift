import UIKit

/// The strip above the keyboard: a row of inline suggestions over the keys an
/// iOS keyboard does not have.
///
/// UIKit fixes an input accessory's height when it first presents it, so the
/// bar is always two rows tall. The top row is the suggestion row while the
/// core has something to say, and a second bank of keys (home/end, page keys,
/// readline control keys) otherwise; the bottom row always holds the keys
/// needed mid-command. Nothing changes height, so the keyboard never jumps.
final class TerminalInputBar: UIInputView {
    let keyBar = KeyBarView(keys: KeyBarView.essentialKeys, includesClipboard: true)
    let secondaryKeys = KeyBarView(keys: KeyBarView.secondaryKeys, includesClipboard: false)

    /// Called with the suggestion the user tapped; the caller types it.
    var onSuggestion: ((CompletionCandidate) -> Void)?

    private let suggestions = SuggestionBarView()
    private var showsSuggestions = false

    static let height: CGFloat = 84
    private static let keyBarHeight: CGFloat = 44
    private static let suggestionHeight: CGFloat = 40

    init() {
        super.init(frame: CGRect(x: 0, y: 0, width: 0, height: Self.height), inputViewStyle: .keyboard)
        autoresizingMask = .flexibleWidth

        suggestions.translatesAutoresizingMaskIntoConstraints = false
        keyBar.translatesAutoresizingMaskIntoConstraints = false
        secondaryKeys.translatesAutoresizingMaskIntoConstraints = false
        suggestions.accessibilityIdentifier = "suggestionBar"
        suggestions.isHidden = true
        addSubview(suggestions)
        addSubview(secondaryKeys)
        addSubview(keyBar)
        NSLayoutConstraint.activate([
            keyBar.leadingAnchor.constraint(equalTo: leadingAnchor),
            keyBar.trailingAnchor.constraint(equalTo: trailingAnchor),
            keyBar.bottomAnchor.constraint(equalTo: bottomAnchor),
            keyBar.heightAnchor.constraint(equalToConstant: Self.keyBarHeight),
            secondaryKeys.leadingAnchor.constraint(equalTo: leadingAnchor),
            secondaryKeys.trailingAnchor.constraint(equalTo: trailingAnchor),
            secondaryKeys.topAnchor.constraint(equalTo: topAnchor),
            secondaryKeys.bottomAnchor.constraint(equalTo: keyBar.topAnchor),
            suggestions.leadingAnchor.constraint(equalTo: leadingAnchor),
            suggestions.trailingAnchor.constraint(equalTo: trailingAnchor),
            suggestions.bottomAnchor.constraint(equalTo: keyBar.topAnchor),
            suggestions.heightAnchor.constraint(equalToConstant: Self.suggestionHeight),
        ])
        suggestions.onPick = { [weak self] candidate in
            self?.onSuggestion?(candidate)
        }
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    /// Swaps the top row between suggestions and the secondary keys. Called
    /// for every answer, but only touches the view hierarchy on a change.
    func show(_ candidates: [CompletionCandidate]) {
        let visible = !candidates.isEmpty
        suggestions.setCandidates(candidates)
        guard visible != showsSuggestions else { return }
        showsSuggestions = visible
        suggestions.isHidden = !visible
        secondaryKeys.isHidden = visible
    }

    /// One-line state for the on-screen latency probe.
    var probeState: String {
        "h=\(Int(frame.height)) shown=\(showsSuggestions ? 1 : 0) super=\(superview?.bounds.height ?? -1)"
    }
}

/// A horizontally scrolling row of suggestion chips. Each chip shows what would
/// be typed and, when the catalog has one, a short description.
final class SuggestionBarView: UIView {
    var onPick: ((CompletionCandidate) -> Void)?

    private let scroll = UIScrollView()
    private let stack = UIStackView()
    private var candidates: [CompletionCandidate] = []
    /// The last list rendered, so a repeated answer does not rebuild the row
    /// and reset its scroll position.
    private var rendered: [String] = []

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = UIColor { traits in
            if traits.userInterfaceStyle == .dark {
                return UIColor(red: 0.16, green: 0.165, blue: 0.18, alpha: 1)
            }
            return UIColor(red: 0.90, green: 0.905, blue: 0.915, alpha: 1)
        }

        scroll.showsHorizontalScrollIndicator = false
        scroll.alwaysBounceHorizontal = true
        scroll.translatesAutoresizingMaskIntoConstraints = false
        addSubview(scroll)

        stack.axis = .horizontal
        stack.spacing = 6
        stack.translatesAutoresizingMaskIntoConstraints = false
        scroll.addSubview(stack)

        NSLayoutConstraint.activate([
            scroll.leadingAnchor.constraint(equalTo: leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: trailingAnchor),
            scroll.topAnchor.constraint(equalTo: topAnchor),
            scroll.bottomAnchor.constraint(equalTo: bottomAnchor),
            stack.leadingAnchor.constraint(equalTo: scroll.contentLayoutGuide.leadingAnchor, constant: 8),
            stack.trailingAnchor.constraint(equalTo: scroll.contentLayoutGuide.trailingAnchor, constant: -8),
            stack.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor, constant: 5),
            stack.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor, constant: -5),
            stack.heightAnchor.constraint(equalTo: scroll.frameLayoutGuide.heightAnchor, constant: -10),
        ])
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    func setCandidates(_ candidates: [CompletionCandidate]) {
        let keys = candidates.map { "\($0.kind)\u{0}\($0.insert)\u{0}\($0.desc ?? "")" }
        guard keys != rendered else { return }
        rendered = keys
        self.candidates = candidates

        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
        // Every match the core returns is rendered; the row scrolls
        // horizontally, so a broad prefix can show the whole list instead of
        // stopping at the first screenful.
        for (index, candidate) in candidates.enumerated() {
            let button = makeChip(candidate)
            button.tag = index
            button.accessibilityIdentifier = "suggestion.\(index)"
            button.addTarget(self, action: #selector(chipTapped(_:)), for: .touchUpInside)
            stack.addArrangedSubview(button)
        }
        scroll.setContentOffset(.zero, animated: false)
    }

    @objc private func chipTapped(_ sender: UIButton) {
        guard sender.tag >= 0, sender.tag < candidates.count else { return }
        onPick?(candidates[sender.tag])
    }

    private func makeChip(_ candidate: CompletionCandidate) -> UIButton {
        // A dynamic colour is resolved per trait; the gray button style would
        // let the terminal show through the chip.
        let fill = UIColor { traits in
            if traits.userInterfaceStyle == .dark {
                return UIColor(red: 0.27, green: 0.275, blue: 0.30, alpha: 1)
            }
            return UIColor(red: 0.97, green: 0.97, blue: 0.98, alpha: 1)
        }
        var configuration = UIButton.Configuration.filled()
        configuration.baseBackgroundColor = fill
        configuration.baseForegroundColor = .label
        configuration.title = chipTitle(candidate)
        if let desc = candidate.desc, !desc.isEmpty {
            configuration.subtitle = desc
        }
        configuration.titleAlignment = .leading
        configuration.cornerStyle = .medium
        configuration.contentInsets = NSDirectionalEdgeInsets(top: 2, leading: 9, bottom: 2, trailing: 9)
        configuration.titleLineBreakMode = .byTruncatingTail
        configuration.subtitleLineBreakMode = .byTruncatingTail
        configuration.titleTextAttributesTransformer = UIConfigurationTextAttributesTransformer { incoming in
            var outgoing = incoming
            outgoing.font = UIFont.monospacedSystemFont(ofSize: 13, weight: .medium)
            return outgoing
        }
        configuration.subtitleTextAttributesTransformer = UIConfigurationTextAttributesTransformer { incoming in
            var outgoing = incoming
            outgoing.font = UIFont.systemFont(ofSize: 10.5)
            outgoing.foregroundColor = .secondaryLabel
            return outgoing
        }
        let button = UIButton(configuration: configuration)
        button.titleLabel?.lineBreakMode = .byTruncatingTail
        button.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        button.accessibilityLabel = [chipTitle(candidate), candidate.desc]
            .compactMap { $0 }
            .joined(separator: ". ")
        return button
    }

    /// What the chip shows: the complete candidate, not just the part that
    /// will be typed. A prefix like "ne" shows "netstat"; tapping it still
    /// inserts only "tstat".
    private func chipTitle(_ candidate: CompletionCandidate) -> String {
        let text = candidate.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !text.isEmpty { return text }
        return candidate.insert.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
