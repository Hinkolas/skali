import Charts
import SkaliKit
import SwiftUI

// The pieces of a project's overview: the usage cards, the storage card
// and its breakdown, the service rows and the environment picker.

extension UsageStat.Kind {
    var tint: Color {
        switch self {
        case .requests: .accentColor
        case .cpu: .usageCPU
        case .memory: .usageMemory
        case .traffic: .green
        case .replicas: .green
        case .restarts: .orange
        }
    }
}

extension ServiceStorage.Kind {
    /// The Studio's storage colors: volumes in the application lavender,
    /// databases cyan, buckets amber, temporary storage pink.
    var tint: Color {
        switch self {
        case .volume: .serviceApplication
        case .database: .cyan
        case .bucket: .orange
        case .temporary: .pink
        }
    }

    var label: String {
        switch self {
        case .volume: "Volumes"
        case .database: "Databases"
        case .bucket: "Buckets"
        case .temporary: "Temporary"
        }
    }

    var systemImage: String {
        switch self {
        case .volume: "internaldrive"
        case .database: ServiceKind.database.systemImage
        case .bucket: ServiceKind.bucket.systemImage
        case .temporary: "hourglass"
        }
    }
}

// MARK: Usage

/// The usage cards side by side, two to a screen; the rest swipe in from
/// the trailing edge, where a sliver of the next one shows.
struct UsageCarousel: View {
    let stats: [UsageStat]
    /// Opens the usage history when a card is tapped.
    var link: ApplicationUsageLink?

    private let spacing: CGFloat = 12
    /// How much of the third card shows.
    private let peek: CGFloat = 28

    var body: some View {
        ScrollView(.horizontal) {
            LazyHStack(spacing: spacing) {
                ForEach(stats) { stat in
                    card(stat)
                        .containerRelativeFrame(.horizontal) { length, _ in
                            (length - spacing - peek) / 2
                        }
                }
            }
            .scrollTargetLayout()
        }
        .scrollTargetBehavior(.viewAligned)
        .scrollIndicators(.hidden)
        // The cards run out to the screen edge instead of stopping at the
        // list's margin.
        .scrollClipDisabled()
    }

    @ViewBuilder private func card(_ stat: UsageStat) -> some View {
        if let link {
            NavigationLink(value: link) { UsageStatCard(stat: stat) }
                .buttonStyle(.plain)
        } else {
            UsageStatCard(stat: stat)
        }
    }
}

/// One usage number over the window's sparkline.
struct UsageStatCard: View {
    let stat: UsageStat

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(stat.kind.title.uppercased())
                .font(.caption2.monospaced().weight(.medium))
                .foregroundStyle(.secondary)
            HStack(alignment: .firstTextBaseline, spacing: 4) {
                Text(stat.value ?? "—")
                    .font(.title2.weight(.bold).monospacedDigit())
                    .contentTransition(.numericText())
                Text(stat.unit)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            .lineLimit(1)
            .minimumScaleFactor(0.7)
            Sparkline(points: stat.points, tint: stat.kind.tint)
                .frame(height: 34)
            Text(stat.footnote)
                .font(.caption2.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.secondarySystemGroupedBackground), in: .rect(cornerRadius: 20))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(stat.kind.title)
        .accessibilityValue(stat.value.map { "\($0) \(stat.unit), \(stat.footnote)" } ?? "No data yet")
    }
}

/// A series as a filled line without axes; gaps stay gaps.
struct Sparkline: View {
    let points: [Double?]
    let tint: Color

    private struct Point: Identifiable {
        let id: Int
        let value: Double
    }

    var body: some View {
        let values = points.enumerated().compactMap { index, value in value.map { Point(id: index, value: $0) } }
        let peak = values.map(\.value).max() ?? 0
        Chart(values) { point in
            AreaMark(x: .value("Time", point.id), y: .value("Value", point.value))
                .foregroundStyle(.linearGradient(
                    colors: [tint.opacity(0.35), tint.opacity(0.02)], startPoint: .top, endPoint: .bottom))
                .interpolationMethod(.monotone)
            LineMark(x: .value("Time", point.id), y: .value("Value", point.value))
                .foregroundStyle(tint)
                .lineStyle(StrokeStyle(lineWidth: 1.5))
                .interpolationMethod(.monotone)
        }
        .chartXScale(domain: 0...max(1, points.count - 1))
        .chartYScale(domain: 0...(peak > 0 ? peak * 1.15 : 1))
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartLegend(.hidden)
    }
}

// MARK: Storage

/// The environment's stored bytes against what its services declared,
/// split by kind.
struct StorageCard: View {
    let rollup: StorageRollup

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline, spacing: 5) {
                Text(formatBytes(rollup.total))
                    .font(.title2.weight(.bold))
                if rollup.capacity > 0 {
                    Text("of \(formatBytes(rollup.capacity))")
                        .foregroundStyle(.secondary)
                }
            }
            StackedBar(
                segments: rollup.byKind.map { .init(color: $0.kind.tint, value: $0.bytes) },
                total: rollup.capacity > 0 ? rollup.capacity : rollup.total)
            FlowLayout(horizontalSpacing: 14, verticalSpacing: 6) {
                ForEach(rollup.byKind, id: \.kind) { entry in
                    HStack(spacing: 5) {
                        Circle().fill(entry.kind.tint).frame(width: 7, height: 7)
                        Text(entry.kind.label)
                            .foregroundStyle(.secondary)
                        Text(formatBytes(entry.bytes))
                    }
                    .font(.caption.monospaced())
                }
            }
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Storage")
        .accessibilityValue(accessibilityValue)
    }

    private var accessibilityValue: String {
        var parts = [rollup.capacity > 0
                     ? "\(formatBytes(rollup.total)) of \(formatBytes(rollup.capacity))"
                     : formatBytes(rollup.total)]
        parts += rollup.byKind.map { "\($0.kind.label) \(formatBytes($0.bytes))" }
        return parts.joined(separator: ", ")
    }
}

/// Shares of one total side by side. A share too small to see still
/// gets a sliver, so every kind in the legend shows in the bar.
struct StackedBar: View {
    struct Segment {
        let color: Color
        let value: Int64
    }

    let segments: [Segment]
    let total: Int64
    var height: CGFloat = 8

    var body: some View {
        GeometryReader { proxy in
            let widths = widths(in: proxy.size.width)
            HStack(spacing: 2) {
                ForEach(Array(zip(segments.indices, widths)), id: \.0) { index, width in
                    Rectangle().fill(segments[index].color).frame(width: width)
                }
                Spacer(minLength: 0)
            }
            .clipShape(.capsule)
            .background(.fill.tertiary, in: .capsule)
        }
        .frame(height: height)
        .accessibilityHidden(true)
    }

    private func widths(in width: CGFloat) -> [CGFloat] {
        guard total > 0 else { return segments.map { _ in 0 } }
        let gaps = CGFloat(max(0, segments.count - 1)) * 2
        let available = max(0, width - gaps)
        return segments.map { segment in
            segment.value > 0 ? max(height * 0.6, available * CGFloat(segment.value) / CGFloat(total)) : 0
        }
    }
}

/// Every storage-consuming service of the environment, largest first.
struct StorageBreakdownView: View {
    let rollup: StorageRollup
    let environment: String

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        List {
            Section {
                ForEach(rollup.services, id: \.serviceKey) { service in
                    StorageServiceRow(service: service)
                }
            } footer: {
                if let sampled = rollup.services.map(\.sampledAt).max() {
                    Text("Sampled \(sampled, format: .relative(presentation: .named)). Databases count their logical size, temporary storage is container layers, logs and scratch space.")
                }
            }
        }
        .navigationTitle("Storage")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) {
                VStack(spacing: 0) {
                    Text("Storage").font(.headline)
                    Text(environment).font(.caption.monospaced()).foregroundStyle(.secondary)
                }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Done", role: .confirm) { dismiss() }
            }
        }
    }
}

struct StorageServiceRow: View {
    let service: ServiceStorage

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 34

    var body: some View {
        HStack(spacing: 12) {
            ResourceTile(systemImage: service.kind.systemImage, tint: service.kind.tint, size: tile)
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .firstTextBaseline) {
                    Text(service.serviceName)
                        .font(.body.monospaced())
                        .lineLimit(1)
                    Spacer(minLength: 8)
                    Text(service.usedBytes.map(formatBytes) ?? "reserved")
                        .font(.subheadline.monospacedDigit())
                }
                if let used = service.usedBytes, service.capacityBytes > 0 {
                    UsageBar(fraction: min(1, Double(used) / Double(service.capacityBytes)), color: service.kind.tint)
                }
                Text(caption)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }

    private var caption: String {
        let kind = service.kind.rawValue
        guard service.capacityBytes > 0 else { return "\(kind) · no limit" }
        return service.usedBytes == nil
            ? "\(kind) · \(formatBytes(service.capacityBytes)) reserved"
            : "\(kind) · of \(formatBytes(service.capacityBytes))"
    }
}

/// Lays its children out in rows, wrapping when a row is full.
struct FlowLayout: Layout {
    var horizontalSpacing: CGFloat = 8
    var verticalSpacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = rows(width: proposal.width ?? .infinity, subviews: subviews)
        let width = rows.map(\.width).max() ?? 0
        let height = rows.map(\.height).reduce(0, +) + CGFloat(max(0, rows.count - 1)) * verticalSpacing
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in rows(width: bounds.width, subviews: subviews) {
            var x = bounds.minX
            for index in row.indices {
                let size = subviews[index].sizeThatFits(.unspecified)
                subviews[index].place(at: CGPoint(x: x, y: y + (row.height - size.height) / 2), proposal: .unspecified)
                x += size.width + horizontalSpacing
            }
            y += row.height + verticalSpacing
        }
    }

    private struct Row {
        var indices: [Int] = []
        var width: CGFloat = 0
        var height: CGFloat = 0
    }

    private func rows(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows: [Row] = []
        var row = Row()
        for index in subviews.indices {
            let size = subviews[index].sizeThatFits(.unspecified)
            let needed = row.indices.isEmpty ? size.width : row.width + horizontalSpacing + size.width
            if needed > width, !row.indices.isEmpty {
                rows.append(row)
                row = Row()
            }
            row.width = row.indices.isEmpty ? size.width : row.width + horizontalSpacing + size.width
            row.height = max(row.height, size.height)
            row.indices.append(index)
        }
        if !row.indices.isEmpty { rows.append(row) }
        return rows
    }
}

// MARK: Services

/// One service of the project: its kind's tile, name, a line on what it
/// is, and how it is doing in the environment.
struct ProjectServiceRow: View {
    let item: ProjectServiceItem

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 36

    var body: some View {
        HStack(spacing: 12) {
            ResourceTile(systemImage: item.kind.systemImage, tint: item.kind.tint, size: tile)
            VStack(alignment: .leading, spacing: 2) {
                Text(item.key)
                    .font(.headline)
                    .lineLimit(1)
                Text(item.detailLine)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            ServiceStateLabel(state: item.state)
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(item.key), \(item.kind.label.lowercased())")
        .accessibilityValue("\(item.detailLine), \(stateDescription)")
    }

    private var stateDescription: String {
        switch item.state {
        case .pending: "loading"
        case .notDeployed: "not deployed"
        case .health(let health): health.label
        }
    }
}

/// "● Healthy" in its color; "Not deployed" when the environment does
/// not run the service.
struct ServiceStateLabel: View {
    let state: ServiceLiveState

    var body: some View {
        switch state {
        case .pending:
            ProgressView().controlSize(.small)
        case .notDeployed:
            Text("Not Deployed")
                .font(.subheadline)
                .foregroundStyle(.secondary)
        case .health(let health):
            Label {
                Text(health.label)
            } icon: {
                Circle().fill(health.tint).frame(width: 7, height: 7)
            }
            .labelStyle(.titleAndIcon)
            .font(.subheadline)
            .foregroundStyle(health.tint)
        }
    }
}

// MARK: Environment picker

/// The environment in the middle of the navigation bar: its health and
/// name, and a menu of the others when there are some.
struct EnvironmentPicker: View {
    let environments: [ProjectEnvironment]
    let selected: ProjectEnvironment
    let health: ServiceHealth?
    let onSelect: (ProjectEnvironment) -> Void

    var body: some View {
        if environments.count > 1 {
            Menu {
                // Toggles rather than a picker: a menu shows a toggle's
                // second line, which carries health and state.
                ForEach(environments) { environment in
                    Toggle(isOn: Binding { environment.id == selected.id } set: { if $0 { onSelect(environment) } }) {
                        Text(environment.name)
                        Text(environment.stateDetail)
                        Image(systemName: environment.symbolName)
                    }
                }
            } label: {
                label(chevron: true)
            }
            .accessibilityLabel("Environment")
            .accessibilityValue(selected.name)
        } else {
            label(chevron: false)
                .accessibilityElement(children: .combine)
                .accessibilityLabel("Environment \(selected.name)")
        }
    }

    private func label(chevron: Bool) -> some View {
        HStack(spacing: 7) {
            Circle()
                .fill(health?.tint ?? .secondary)
                .frame(width: 8, height: 8)
            Text(selected.name)
                .font(.subheadline.monospaced().weight(.medium))
                .foregroundStyle(.primary)
            if chevron {
                Image(systemName: "chevron.up.chevron.down")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
        .glassEffect(chevron ? .regular.interactive() : .regular, in: .capsule)
    }

}

extension ProjectEnvironment {
    /// The environment's symbol in menus and lists: its state when that
    /// matters, else whether it is high priority.
    var symbolName: String {
        if isLocked { return "lock" }
        if state == "releasing" { return "trash" }
        if state == "down" { return "stop.circle" }
        return isHighPriority ? "star" : "square.stack.3d.up"
    }

    /// "healthy · active", "locked".
    var stateDetail: String {
        if isLocked { return "locked" }
        return [health.map { $0.label.lowercased() }, stateLabel].compactMap(\.self).joined(separator: " · ")
    }
}
