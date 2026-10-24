import Foundation

/// What a session's User-Agent says about the device behind it, for the
/// session list. Recognizes skali's own clients first, then the browser
/// and system pairs the Studio recognizes (studio/src/lib/useragent.ts).
public struct SessionDevice: Hashable, Sendable {
    public enum Kind: Hashable, Sendable {
        /// The Skali CLI.
        case cli
        /// This app, on an iPhone or an iPad.
        case app
        case browser
        case unknown
    }

    public let kind: Kind
    /// "Skali CLI v0.1.3", "Skali for iOS", "Safari on macOS".
    public let title: String
    /// The machine the client named, e.g. the CLI's hostname or "iPhone".
    public let host: String?
    public let isMobile: Bool
    public let isTablet: Bool

    /// The User-Agent this app sends: `skali-ios/<version> (<model>; <system> <version>)`.
    public static func appUserAgent(version: String, model: String, system: String, systemVersion: String) -> String {
        "skali-ios/\(version) (\(model); \(system) \(systemVersion))"
    }

    public init(userAgent: String?) {
        let ua = userAgent?.trimmingCharacters(in: .whitespaces) ?? ""

        // skali/<version> (<host>), see cmd/skali/main.go.
        if let match = ua.wholeMatch(of: /skali\/(\S+)(?: \((.+)\))?/) {
            kind = .cli
            title = "Skali CLI \(match.1)"
            host = match.2.map(String.init)
            isMobile = false
            isTablet = false
            return
        }
        // skali-ios/<version> (<model>; <system> <version>), sent by this app.
        if let match = ua.wholeMatch(of: /skali-ios\/(\S+)(?: \(([^;)]+)(?:; ([^)]+))?\))?/) {
            let model = match.2.map(String.init)
            let tablet = model?.hasPrefix("iPad") == true
            kind = .app
            title = tablet ? "Skali for iPadOS" : "Skali for iOS"
            host = model
            isMobile = true
            isTablet = tablet
            return
        }

        let browser = Self.browsers.first { ua.contains($0.pattern) }?.name
        let system = Self.systems.first { ua.contains($0.pattern) }?.name
        host = nil
        isTablet = ua.contains("iPad")
        isMobile = ua.contains(/\bMobi|\biPhone\b|\biPad\b|\bAndroid\b/)
        switch (browser, system) {
        case let (browser?, system?):
            kind = .browser
            title = "\(browser) on \(system)"
        case let (browser?, nil):
            kind = .browser
            title = browser
        case let (nil, system?):
            kind = .unknown
            title = system
        case (nil, nil):
            kind = .unknown
            title = ua.isEmpty ? "Unknown device" : ua
        }
    }

    // Order matters: Chrome UAs contain "Safari", Edge and Opera UAs contain
    // "Chrome", so the more specific tokens win first.
    // Computed: Regex is not Sendable, so it cannot live in a static constant.
    private static var browsers: [(pattern: Regex<Substring>, name: String)] { [
        (/\bEdg(?:e|A|iOS)?\//, "Edge"),
        (/\bOPR\/|\bOpera\b/, "Opera"),
        (/\bSamsungBrowser\//, "Samsung Internet"),
        (/\bFirefox\/|\bFxiOS\//, "Firefox"),
        (/\bChrome\/|\bCriOS\/|\bChromium\//, "Chrome"),
        (/\bSafari\//, "Safari"),
    ] }

    // Android before Linux, iPhone and iPad before macOS ("like Mac OS X").
    private static var systems: [(pattern: Regex<Substring>, name: String)] { [
        (/\bWindows\b/, "Windows"),
        (/\biPhone\b|\biPod\b/, "iOS"),
        (/\biPad\b/, "iPadOS"),
        (/\bMac OS X\b|\bMacintosh\b/, "macOS"),
        (/\bAndroid\b/, "Android"),
        (/\bCrOS\b/, "ChromeOS"),
        (/\bLinux\b|\bX11\b/, "Linux"),
    ] }
}
