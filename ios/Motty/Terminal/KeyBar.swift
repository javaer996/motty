import UIKit

/// The strip above the keyboard: the keys an iOS keyboard does not have.
final class KeyBarView: UIInputView {
    struct Key {
        let title: String
        let bytes: [UInt8]
    }

    var onKey: ((Data) -> Void)?
    var onPaste: (() -> Void)?
    var onCopy: (() -> Void)?

    private let keys: [Key] = [
        Key(title: "esc", bytes: [0x1B]),
        Key(title: "tab", bytes: [0x09]),
        Key(title: "ctrl-c", bytes: [0x03]),
        Key(title: "ctrl-d", bytes: [0x04]),
        Key(title: "ctrl-z", bytes: [0x1A]),
        Key(title: "↑", bytes: Array("\u{1B}[A".utf8)),
        Key(title: "↓", bytes: Array("\u{1B}[B".utf8)),
        Key(title: "→", bytes: Array("\u{1B}[C".utf8)),
        Key(title: "←", bytes: Array("\u{1B}[D".utf8)),
        Key(title: "home", bytes: Array("\u{1B}[H".utf8)),
        Key(title: "end", bytes: Array("\u{1B}[F".utf8)),
        Key(title: "pgup", bytes: Array("\u{1B}[5~".utf8)),
        Key(title: "pgdn", bytes: Array("\u{1B}[6~".utf8)),
    ]

    init() {
        super.init(frame: CGRect(x: 0, y: 0, width: 0, height: 44), inputViewStyle: .keyboard)
        autoresizingMask = .flexibleWidth
        build()
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    private func build() {
        let scroll = UIScrollView()
        scroll.showsHorizontalScrollIndicator = false
        scroll.translatesAutoresizingMaskIntoConstraints = false
        addSubview(scroll)

        let stack = UIStackView()
        stack.axis = .horizontal
        stack.spacing = 6
        stack.translatesAutoresizingMaskIntoConstraints = false
        scroll.addSubview(stack)

        for (index, key) in keys.enumerated() {
            let button = makeButton(title: key.title)
            button.tag = index
            button.addTarget(self, action: #selector(keyTapped(_:)), for: .touchUpInside)
            stack.addArrangedSubview(button)
        }
        let paste = makeButton(title: AppLanguage.localized("paste"))
        paste.tag = -1
        paste.addTarget(self, action: #selector(pasteTapped), for: .touchUpInside)
        stack.addArrangedSubview(paste)
        let copy = makeButton(title: AppLanguage.localized("copy"))
        copy.tag = -2
        copy.addTarget(self, action: #selector(copyTapped), for: .touchUpInside)
        stack.addArrangedSubview(copy)

        NSLayoutConstraint.activate([
            scroll.leadingAnchor.constraint(equalTo: leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: trailingAnchor),
            scroll.topAnchor.constraint(equalTo: topAnchor),
            scroll.bottomAnchor.constraint(equalTo: bottomAnchor),
            stack.leadingAnchor.constraint(equalTo: scroll.contentLayoutGuide.leadingAnchor, constant: 8),
            stack.trailingAnchor.constraint(equalTo: scroll.contentLayoutGuide.trailingAnchor, constant: -8),
            stack.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor, constant: 6),
            stack.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor, constant: -6),
            stack.heightAnchor.constraint(equalTo: scroll.frameLayoutGuide.heightAnchor, constant: -12),
        ])
    }

    private func makeButton(title: String) -> UIButton {
        var configuration = UIButton.Configuration.gray()
        configuration.title = title
        configuration.baseForegroundColor = .label
        configuration.cornerStyle = .small
        configuration.contentInsets = NSDirectionalEdgeInsets(top: 2, leading: 10, bottom: 2, trailing: 10)
        configuration.titleTextAttributesTransformer = UIConfigurationTextAttributesTransformer { incoming in
            var outgoing = incoming
            outgoing.font = UIFont.monospacedSystemFont(ofSize: 13, weight: .medium)
            return outgoing
        }
        return UIButton(configuration: configuration)
    }

    @objc private func keyTapped(_ sender: UIButton) {
        guard sender.tag >= 0, sender.tag < keys.count else { return }
        onKey?(Data(keys[sender.tag].bytes))
    }

    @objc private func pasteTapped() {
        onPaste?()
    }

    @objc private func copyTapped() {
        onCopy?()
    }
}
