import SwiftUI

/// The languages the desktop version offers, plus the system default. The
/// choice is stored by raw value so `@AppStorage` can drive both the picker
/// and the locale applied at the root of the app.
enum AppLanguage: String, CaseIterable, Identifiable {
    case system
    case english
    case simplifiedChinese
    case traditionalChinese

    static let storageKey = "motty.language"

    var id: String { rawValue }

    /// Names are shown in their own language, as on the desktop.
    var title: LocalizedStringKey {
        switch self {
        case .system: return "Follow System"
        case .english: return "English"
        case .simplifiedChinese: return "简体中文"
        case .traditionalChinese: return "繁體中文"
        }
    }

    /// nil leaves the choice to iOS, which resolves it from the preferred
    /// languages in Settings.
    var locale: Locale? {
        switch self {
        case .system: return nil
        case .english: return Locale(identifier: "en")
        case .simplifiedChinese: return Locale(identifier: "zh-Hans")
        case .traditionalChinese: return Locale(identifier: "zh-Hant")
        }
    }

    static var current: AppLanguage {
        AppLanguage(rawValue: UserDefaults.standard.string(forKey: storageKey) ?? "") ?? .system
    }

    /// For formatters built outside SwiftUI, which cannot read the
    /// environment locale.
    static var effectiveLocale: Locale {
        current.locale ?? .autoupdatingCurrent
    }

    /// Resolves a string immediately, for messages built outside a SwiftUI
    /// body (for example in an RPC callback).
    ///
    /// `String(localized:locale:)` lets the bundle pick the language and only
    /// uses the locale for formatting, so an in-app choice must be applied by
    /// loading that language's `.lproj` bundle. English is the source
    /// language: an empty bundle makes the key itself the result.
    static func localized(_ value: String.LocalizationValue) -> String {
        String(localized: value, bundle: resourceBundle(), locale: effectiveLocale)
    }

    private static let sourceBundle: Bundle = {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("MottySourceLanguage", isDirectory: true)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return Bundle(url: directory) ?? Bundle.main
    }()

    private static func resourceBundle() -> Bundle {
        switch current {
        case .english:
            return sourceBundle
        case .simplifiedChinese:
            return lprojBundle("zh-Hans") ?? Bundle.main
        case .traditionalChinese:
            return lprojBundle("zh-Hant") ?? Bundle.main
        case .system:
            let preferred = Bundle.main.preferredLocalizations.first ?? "en"
            if preferred.hasPrefix("zh-Hant") || preferred.hasPrefix("zh-TW")
                || preferred.hasPrefix("zh-HK") || preferred.hasPrefix("zh-MO") {
                return lprojBundle("zh-Hant") ?? Bundle.main
            }
            if preferred.hasPrefix("zh-Hans") || preferred.hasPrefix("zh-CN")
                || preferred.hasPrefix("zh-SG") || preferred == "zh" {
                return lprojBundle("zh-Hans") ?? Bundle.main
            }
            return sourceBundle
        }
    }

    private static func lprojBundle(_ tag: String) -> Bundle? {
        guard let path = Bundle.main.path(forResource: tag, ofType: "lproj") else { return nil }
        return Bundle(path: path)
    }
}
