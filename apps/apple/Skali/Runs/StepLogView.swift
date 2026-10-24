import SkaliKit
import SwiftUI

/// One step's log: what was written so far, then, while the step runs,
/// each new line as it is written.
struct StepLogView: View {
    let step: RunStep
    let api: any ClusterAPI

    @State private var entries: [LogEntry] = []
    @State private var loaded = false
    @State private var error: String?

    /// Pages of 500 the screen reads at most; a longer log keeps its end
    /// in the Studio or the CLI.
    private let maxPages = 20

    var body: some View {
        ScrollViewReader { proxy in
            List {
                Section {
                    ForEach(entries) { entry in
                        LogLine(entry: entry)
                            .id(entry.id)
                    }
                } header: {
                    HStack(spacing: 8) {
                        StepStatusIcon(step: step)
                        Text(step.status)
                    }
                }
            }
            .listStyle(.plain)
            // Lines sit as close as in a terminal, not at row height.
            .environment(\.defaultMinListRowHeight, 0)
            .onChange(of: entries.count) {
                guard step.isActive, let last = entries.last else { return }
                withAnimation { proxy.scrollTo(last.id, anchor: .bottom) }
            }
        }
        .overlay {
            if let error, entries.isEmpty {
                ContentUnavailableView("Log Unavailable", systemImage: "exclamationmark.triangle", description: Text(error))
            } else if !loaded {
                ProgressView()
            } else if entries.isEmpty {
                ContentUnavailableView("No Log Output", systemImage: "text.alignleft")
            }
        }
        .navigationTitle(step.title)
        .navigationBarTitleDisplayMode(.inline)
        .task(id: step.id) { await load() }
    }

    private func load() async {
        var cursor: String?
        do {
            for _ in 0..<maxPages {
                let page = try await api.stepLogs(stepID: step.id, after: cursor)
                entries += page.logs
                cursor = page.next ?? cursor
                if page.logs.isEmpty || page.next == nil { break }
            }
        } catch is CancellationError {
            return
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
        guard step.isActive else { return }
        do {
            for try await entry in api.stepLogUpdates(stepID: step.id, after: cursor) {
                entries.append(entry)
            }
        } catch {}
    }
}

/// A log line: its time and message, colored by level.
private struct LogLine: View {
    let entry: LogEntry

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(entry.ts, format: .dateTime.hour(.twoDigits(amPM: .omitted)).minute(.twoDigits).second(.twoDigits))
                .font(.caption2.monospaced())
                .foregroundStyle(.tertiary)
            Text(entry.message)
                .font(.caption.monospaced())
                .foregroundStyle(tint)
                .typesetAsCode()
                .textSelection(.enabled)
        }
        .listRowSeparator(.hidden)
        .listRowInsets(EdgeInsets(top: 3, leading: 16, bottom: 3, trailing: 16))
    }

    private var tint: Color {
        switch entry.level {
        case "error": .red
        case "warn": .orange
        case "debug": .secondary
        default: .primary
        }
    }
}
