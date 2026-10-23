import Charts
import SkaliKit
import SwiftUI

// The building blocks the resource screens share (nodes, database pools):
// a glyph tile, usage bars and tiles, a status pill, the detail header and
// a usage chart.

extension Color {
    /// CPU in the accent's lavender, memory in blue, disk in amber, as the
    /// Studio's usage bars.
    static let usageCPU = Color.serviceApplication
    static let usageMemory = Color.cyan
    static let usageDisk = Color.orange
}

/// A glyph on its tinted, outlined tile.
struct ResourceTile: View {
    let systemImage: String
    let tint: Color
    var size: CGFloat

    var body: some View {
        Image(systemName: systemImage)
            .font(.system(size: size * 0.46, weight: .medium))
            .foregroundStyle(tint)
            .frame(width: size, height: size)
            .background(tint.opacity(0.15), in: .rect(cornerRadius: size * 0.27))
            .overlay(RoundedRectangle(cornerRadius: size * 0.27).strokeBorder(tint.opacity(0.25)))
            .accessibilityHidden(true)
    }
}

/// A thin usage bar; empty when the share is unknown.
struct UsageBar: View {
    let fraction: Double?
    let color: Color
    var height: CGFloat = 4

    var body: some View {
        Capsule()
            .fill(.fill.tertiary)
            .overlay(alignment: .leading) {
                GeometryReader { proxy in
                    if let fraction {
                        Capsule()
                            .fill(color)
                            .frame(width: max(height, proxy.size.width * fraction))
                    }
                }
            }
            .frame(height: height)
            .animation(.default, value: fraction)
    }
}

/// The two labeled mini bars at the end of a resource row.
struct RowUsageBars: View {
    struct Bar {
        let label: String
        let fraction: Double?
        let color: Color
    }

    let bars: [Bar]

    @ScaledMetric(relativeTo: .caption2) private var width: CGFloat = 52

    var body: some View {
        Grid(alignment: .leading, horizontalSpacing: 6, verticalSpacing: 5) {
            ForEach(bars, id: \.label) { bar in
                GridRow {
                    Text(bar.label)
                    UsageBar(fraction: bar.fraction, color: bar.color)
                        .frame(width: width)
                }
            }
        }
        .font(.caption2.monospaced())
        .foregroundStyle(.secondary)
        .accessibilityHidden(true)
    }
}

/// "● Online", "● Refusing": a state in its color.
struct StatusCapsule: View {
    let text: String
    let tint: Color

    var body: some View {
        Label {
            Text(text)
        } icon: {
            Circle().fill(tint).frame(width: 7, height: 7)
        }
        .labelStyle(.titleAndIcon)
        .font(.subheadline.weight(.medium))
        .foregroundStyle(tint)
        .padding(.horizontal, 10)
        .padding(.vertical, 4)
        .background(tint.opacity(0.15), in: .capsule)
        .fixedSize()
    }
}

/// The top of a resource's screen: tile, name, state and a short line.
struct ResourceHeader: View {
    let systemImage: String
    let tint: Color
    let name: String
    let status: StatusCapsule
    let detail: String

    var body: some View {
        VStack(spacing: 10) {
            ResourceTile(systemImage: systemImage, tint: tint, size: 72)
            Text(name)
                .font(.title2.monospaced().weight(.bold))
                .lineLimit(1)
                .minimumScaleFactor(0.6)
            HStack(spacing: 10) {
                status
                Text(detail)
                    .font(.subheadline.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .minimumScaleFactor(0.8)
            }
        }
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
    }
}

/// One headline number: a label, the value, a bar and its denominator.
struct UsageTile: View {
    let title: String
    let value: String
    let fraction: Double?
    let color: Color
    let caption: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title.uppercased())
                .font(.caption2.monospaced().weight(.medium))
                .foregroundStyle(.secondary)
            Text(value)
                .font(.title2.weight(.bold).monospacedDigit())
                .lineLimit(1)
                .minimumScaleFactor(0.7)
                .contentTransition(.numericText())
            UsageBar(fraction: fraction, color: color, height: 5)
            Text(caption)
                .font(.caption2.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.secondarySystemGroupedBackground), in: .rect(cornerRadius: 20))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(title)
        .accessibilityValue("\(value), \(caption)")
    }

    /// "27%" for a share, "—" when it is unknown.
    static func percent(_ fraction: Double?) -> String {
        fraction.map { "\(Int(($0 * 100).rounded()))%" } ?? "—"
    }
}

/// A row of usage tiles, edge to edge in a list section.
struct UsageTileRow<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        HStack(spacing: 10) { content }
    }
}

/// One line of a usage chart.
struct MetricSeries: Identifiable {
    let label: String
    let color: Color
    let points: [(date: Date, value: Double?)]

    var id: String { label }
}

/// Usage over a window, one or more lines on a shared axis.
struct MetricChart: View {
    let series: [MetricSeries]
    let window: NodeMetrics.Window
    var yDomain: ClosedRange<Double>?
    let format: (Double) -> String

    private struct Point: Identifiable {
        let id: String
        let date: Date
        let series: String
        let value: Double
    }

    var body: some View {
        let points = series.flatMap { line in
            line.points.enumerated().compactMap { index, point in
                point.value.map { Point(id: "\(line.label)-\(index)", date: point.date, series: line.label, value: $0) }
            }
        }
        if points.isEmpty {
            ContentUnavailableView("No Samples Yet", systemImage: "chart.xyaxis.line")
        } else {
            chart(points)
        }
    }

    @ViewBuilder private func chart(_ points: [Point]) -> some View {
        let base = Chart(points) { point in
            LineMark(x: .value("Time", point.date), y: .value("Value", point.value))
                .foregroundStyle(by: .value("Series", point.series))
                .interpolationMethod(.monotone)
        }
        .chartForegroundStyleScale(domain: series.map(\.label), range: series.map(\.color))
        .chartLegend(series.count > 1 ? .visible : .hidden)
        .chartLegend(position: .top, alignment: .leading)
        .chartYAxis {
            AxisMarks(values: .automatic(desiredCount: 3)) { value in
                AxisGridLine()
                AxisValueLabel {
                    if let number = value.as(Double.self) { Text(format(number)) }
                }
            }
        }
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 4)) { _ in
                AxisGridLine()
                AxisValueLabel(format: window == .week ? .dateTime.weekday(.abbreviated) : .dateTime.hour().minute())
            }
        }
        if let yDomain {
            base.chartYScale(domain: yDomain)
        } else {
            base
        }
    }
}

/// The window picker above a usage chart.
struct MetricWindowPicker: View {
    @Binding var window: NodeMetrics.Window

    var body: some View {
        Picker("Window", selection: $window) {
            ForEach(NodeMetrics.Window.allCases) { window in
                Text(window.label).tag(window)
            }
        }
        .pickerStyle(.segmented)
    }
}

extension NodeMetrics.Window {
    var label: String {
        switch self {
        case .hour: "1 Hour"
        case .day: "24 Hours"
        case .week: "7 Days"
        }
    }
}
