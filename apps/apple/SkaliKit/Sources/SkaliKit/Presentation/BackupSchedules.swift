import Foundation

// Automatic backup schedules: the cron expression in words, the shapes
// the schedule editor offers, and when the next one runs. Cron is
// evaluated in UTC, as skalid does (studio/src/lib/cron.ts).

/// "daily at 03:00", "every 6 hours", "weekly on Sunday at 03:00"; the
/// expression itself for shapes without words.
public func describeCron(_ expression: String) -> String {
    let fields = expression.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
    guard fields.count == 5, fields[3] == "*" else { return expression }
    let (minute, hour, day, weekday) = (fields[0], fields[1], fields[2], fields[4])
    func isNumber(_ field: String) -> Bool { !field.isEmpty && field.allSatisfy(\.isNumber) }
    func step(_ field: String) -> Int? { field.hasPrefix("*/") ? Int(field.dropFirst(2)) : nil }
    func clock(_ hour: String, _ minute: String) -> String {
        String(format: "%02d:%02d", Int(hour) ?? 0, Int(minute) ?? 0)
    }
    if day == "*" && weekday == "*" {
        if minute == "*" && hour == "*" { return "every minute" }
        if let minutes = step(minute), hour == "*" { return minutes == 1 ? "every minute" : "every \(minutes) minutes" }
        if let hours = step(hour), isNumber(minute) { return hours == 1 ? "hourly" : "every \(hours) hours" }
        if isNumber(minute) && hour == "*" { return String(format: "hourly at :%02d", Int(minute) ?? 0) }
        if isNumber(minute) && isNumber(hour) { return "daily at \(clock(hour, minute))" }
        return expression
    }
    if day == "*", let weekdayNumber = Int(weekday), isNumber(minute), isNumber(hour) {
        return "weekly on \(ScheduleDraft.weekdayNames[weekdayNumber % 7]) at \(clock(hour, minute))"
    }
    if weekday == "*", let dayNumber = Int(day), isNumber(minute), isNumber(hour) {
        return "monthly on the \(ordinal(dayNumber)) at \(clock(hour, minute))"
    }
    return expression
}

/// "14 days", "36 hours", "45 minutes".
public func describeRetention(seconds: Int64) -> String {
    func unit(_ count: Int64, _ word: String) -> String { "\(count) \(word)\(count == 1 ? "" : "s")" }
    if seconds % 86_400 == 0 { return unit(seconds / 86_400, "day") }
    if seconds % 3600 == 0 { return unit(seconds / 3600, "hour") }
    return unit((seconds + 30) / 60, "minute")
}

private func ordinal(_ number: Int) -> String {
    switch (number % 100, number % 10) {
    case (11...13, _): "\(number)th"
    case (_, 1): "\(number)st"
    case (_, 2): "\(number)nd"
    case (_, 3): "\(number)rd"
    default: "\(number)th"
    }
}

/// A schedule as the editor shows it: a repeat, a time of day and the
/// retention. Expressions it has no shape for stay as typed (`custom`).
public struct ScheduleDraft: Hashable, Sendable {
    public enum Repeat: String, CaseIterable, Identifiable, Sendable {
        case hourly, every6Hours, every12Hours, daily, weekly, monthly, custom

        public var id: String { rawValue }

        public var label: String {
            switch self {
            case .hourly: "Hourly"
            case .every6Hours: "Every 6 Hours"
            case .every12Hours: "Every 12 Hours"
            case .daily: "Daily"
            case .weekly: "Weekly"
            case .monthly: "Monthly"
            case .custom: "Custom"
            }
        }

        /// Whether the hour matters: the hourly shapes take only a minute.
        public var usesHour: Bool { ![.hourly, .every6Hours, .every12Hours, .custom].contains(self) }
    }

    public static let weekdayNames = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]

    /// The retentions the editor offers, in seconds.
    public static let retentionChoices: [Int64] = [1, 3, 7, 14, 30, 90, 365].map { $0 * 86_400 }

    public var repeatEvery: Repeat
    public var hour: Int
    public var minute: Int
    /// 0 is Sunday, as in cron.
    public var weekday: Int
    public var dayOfMonth: Int
    /// The expression for `custom`.
    public var expression: String
    public var retentionSeconds: Int64

    /// Daily at 03:00, kept for a week: the Studio's default.
    public init(schedule: BackupSchedule? = nil) {
        repeatEvery = .daily
        hour = 3
        minute = 0
        weekday = 0
        dayOfMonth = 1
        expression = schedule?.schedule ?? "0 3 * * *"
        retentionSeconds = schedule?.retentionSeconds ?? 7 * 86_400
        if let schedule { read(schedule.schedule) }
    }

    private mutating func read(_ cron: String) {
        repeatEvery = .custom
        let fields = cron.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
        guard fields.count == 5, fields[3] == "*", let minute = Int(fields[0]), (0..<60).contains(minute) else { return }
        let (hourField, day, weekdayField) = (fields[1], fields[2], fields[4])
        self.minute = minute
        switch (hourField, day, weekdayField) {
        case ("*", "*", "*"): repeatEvery = .hourly
        case ("*/6", "*", "*"): repeatEvery = .every6Hours
        case ("*/12", "*", "*"): repeatEvery = .every12Hours
        default:
            guard let hour = Int(hourField), (0..<24).contains(hour) else { return }
            self.hour = hour
            if day == "*" && weekdayField == "*" {
                repeatEvery = .daily
            } else if day == "*", let weekday = Int(weekdayField), (0...7).contains(weekday) {
                self.weekday = weekday % 7
                repeatEvery = .weekly
            } else if weekdayField == "*", let dayOfMonth = Int(day), (1...28).contains(dayOfMonth) {
                self.dayOfMonth = dayOfMonth
                repeatEvery = .monthly
            }
        }
    }

    /// The five-field expression, in UTC.
    public var cron: String {
        switch repeatEvery {
        case .hourly: "\(minute) * * * *"
        case .every6Hours: "\(minute) */6 * * *"
        case .every12Hours: "\(minute) */12 * * *"
        case .daily: "\(minute) \(hour) * * *"
        case .weekly: "\(minute) \(hour) * * \(weekday)"
        case .monthly: "\(minute) \(hour) \(dayOfMonth) * *"
        case .custom: expression.split(separator: " ", omittingEmptySubsequences: true).joined(separator: " ")
        }
    }

    /// Five fields; the daemon checks the rest and says what it refuses.
    public var isValid: Bool {
        cron.split(separator: " ").count == 5
    }

    public var schedule: BackupSchedule {
        BackupSchedule(schedule: cron, retentionSeconds: retentionSeconds)
    }

    /// When a schedule of a shape the editor knows runs next after `date`;
    /// nil for custom expressions.
    public func nextRun(after date: Date) -> Date? {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        switch repeatEvery {
        case .hourly:
            return calendar.nextDate(after: date, matching: DateComponents(minute: minute), matchingPolicy: .nextTime)
        case .every6Hours, .every12Hours:
            let step = repeatEvery == .every6Hours ? 6 : 12
            return (0..<24).filter { $0 % step == 0 }
                .compactMap { calendar.nextDate(after: date, matching: DateComponents(hour: $0, minute: minute), matchingPolicy: .nextTime) }
                .min()
        case .daily:
            return calendar.nextDate(after: date, matching: DateComponents(hour: hour, minute: minute), matchingPolicy: .nextTime)
        case .weekly:
            return calendar.nextDate(
                after: date, matching: DateComponents(hour: hour, minute: minute, weekday: weekday + 1),
                matchingPolicy: .nextTime)
        case .monthly:
            return calendar.nextDate(
                after: date, matching: DateComponents(day: dayOfMonth, hour: hour, minute: minute),
                matchingPolicy: .nextTime)
        case .custom:
            return nil
        }
    }
}
