import SkaliKit
import SwiftUI

/// An application's log as its pods write it, every pod in one stream.
/// Pausing holds new lines back until resumed; a dropped stream reconnects.
struct RuntimeLogsView: View {
    let key: String
    let model: ProjectModel

    private struct Line: Identifiable {
        let id: Int
        let pod: String
        let text: String
        let time: Date?
        let previous: Bool
    }

    /// Lines kept on screen; older ones scroll away for good.
    private static let capacity = 2000

    @State private var lines: [Line] = []
    @State private var held: [Line] = []
    @State private var paused = false
    @State private var pod: String?
    @State private var counter = 0
    @State private var unavailable: String?
    @State private var connected = false

    var body: some View {
        ScrollViewReader { proxy in
            List(shown) { line in
                LogRow(pod: line.pod, text: line.text, time: line.time, previous: line.previous, tint: tint(line.pod))
                    .id(line.id)
                    .listRowSeparator(.hidden)
                    .listRowInsets(EdgeInsets(top: 4, leading: 14, bottom: 4, trailing: 14))
            }
            .listStyle(.plain)
            .environment(\.defaultMinListRowHeight, 0)
            .onChange(of: lines.last?.id) {
                guard !paused, let last = shown.last else { return }
                proxy.scrollTo(last.id, anchor: .bottom)
            }
        }
        .overlay {
            if let unavailable, lines.isEmpty {
                ContentUnavailableView("Logs Unavailable", systemImage: "text.alignleft", description: Text(unavailable))
            } else if lines.isEmpty {
                ContentUnavailableView {
                    Label(connected ? "Waiting for Output" : "Connecting", systemImage: "text.alignleft")
                } description: {
                    Text("Lines appear as \(key)'s pods write them.")
                }
            }
        }
        .safeAreaInset(edge: .bottom) {
            if paused && !held.isEmpty {
                Button("\(held.count) new \(held.count == 1 ? "line" : "lines")") { resume() }
                    .buttonStyle(.glassProminent)
                    .padding(.bottom, 8)
            }
        }
        .navigationTitle("Logs")
        .navigationSubtitle("\(key) · \(model.environment?.name ?? "")")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button(paused ? "Resume" : "Pause", systemImage: paused ? "play.fill" : "pause.fill") {
                    if paused { resume() } else { paused = true }
                }
            }
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Picker("Pods", selection: $pod) {
                        Text("All Pods").tag(String?.none)
                        ForEach(pods, id: \.self) { name in
                            Text(name).tag(String?.some(name))
                        }
                    }
                    Section {
                        Button("Clear", systemImage: "trash") {
                            lines = []
                            held = []
                        }
                    }
                } label: {
                    Label("More", systemImage: "ellipsis")
                }
            }
        }
        .task(id: model.environment?.id) { await follow() }
    }

    private var shown: [Line] {
        guard let pod else { return lines }
        return lines.filter { $0.pod == pod }
    }

    private var pods: [String] {
        Array(Set(lines.map(\.pod))).sorted()
    }

    /// A color per pod, in the order they first spoke.
    private func tint(_ pod: String) -> Color {
        let palette: [Color] = [.cyan, .serviceApplication, .orange, .green, .pink, .yellow]
        let index = pods.firstIndex(of: pod) ?? 0
        return palette[index % palette.count]
    }

    private func resume() {
        append(held)
        held = []
        paused = false
    }

    private func append(_ new: [Line]) {
        lines.append(contentsOf: new)
        if lines.count > Self.capacity { lines.removeFirst(lines.count - Self.capacity) }
    }

    private func follow() async {
        guard let id = model.environment?.id else { return }
        var delay = Duration.seconds(1)
        while !Task.isCancelled {
            do {
                connected = true
                for try await event in model.api.runtimeLogs(environmentID: id, service: key) {
                    counter += 1
                    let line = Line(id: counter, pod: event.pod, text: event.line, time: event.time, previous: event.previous == true)
                    if paused { held.append(line) } else { append([line]) }
                    unavailable = nil
                    delay = .seconds(1)
                }
            } catch let error as APIError where error.status == 503 || error.status == 403 || error.status == 404 {
                unavailable = error.localizedDescription
                if error.status != 503 { return }
            } catch {}
            connected = false
            try? await Task.sleep(for: delay)
            delay = min(delay * 2, .seconds(15))
        }
    }
}

private struct LogRow: View {
    let pod: String
    let text: String
    let time: Date?
    let previous: Bool
    let tint: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack(spacing: 6) {
                Text(podSuffix)
                    .foregroundStyle(tint)
                if let time {
                    Text(time, format: .dateTime.hour(.twoDigits(amPM: .omitted)).minute(.twoDigits).second(.twoDigits))
                        .foregroundStyle(.tertiary)
                }
                if previous {
                    Text("previous container")
                        .foregroundStyle(.orange)
                }
            }
            .font(.caption2.monospaced())
            Text(text)
                .font(.caption.monospaced())
                .foregroundStyle(previous ? .secondary : .primary)
                .typesetAsCode()
                .textSelection(.enabled)
        }
    }

    /// The part of a pod's name that tells replicas apart: its last block,
    /// cut to the random tail Kubernetes ends pod names with.
    private var podSuffix: String {
        guard let last = pod.split(separator: "-").last else { return pod }
        return String(last.suffix(6))
    }
}
