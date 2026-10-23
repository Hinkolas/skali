import Foundation

/// The terse age listings show: "42s", "7m", "5h", "42d". The Studio's
/// relativeTime (studio/src/lib/format.ts) uses the same steps.
public func compactAge(since date: Date, now: Date = Date()) -> String {
    let seconds = max(0, Int(now.timeIntervalSince(date)))
    switch seconds {
    case ..<60: return "\(seconds)s"
    case ..<3600: return "\(seconds / 60)m"
    case ..<86_400: return "\(seconds / 3600)h"
    default: return "\(seconds / 86_400)d"
    }
}
