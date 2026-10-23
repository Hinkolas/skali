import SkaliKit
import SwiftUI

/// An environment's automatic backups: on or off, how often and when (in
/// UTC, as the daemon runs them), and how long scheduled snapshots stay.
/// Shapes the editor has no control for stay a cron expression.
struct BackupScheduleSheet: View {
    let environment: ProjectEnvironment
    let model: ProjectModel

    @Environment(\.dismiss) private var dismiss
    @State private var enabled: Bool
    @State private var draft: ScheduleDraft
    @State private var saving = false
    @State private var error: String?

    init(environment: ProjectEnvironment, model: ProjectModel) {
        self.environment = environment
        self.model = model
        _enabled = State(initialValue: environment.settings?.backup != nil)
        _draft = State(initialValue: ScheduleDraft(schedule: environment.settings?.backup))
    }

    private var canEdit: Bool { environment.role >= .admin }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Toggle("Automatic Backups", isOn: $enabled.animation())
                        .tint(.accentColor)
                } footer: {
                    if !canEdit {
                        Text("Only admins of \(environment.name) change its schedule.")
                    } else if enabled && model.definition?.hasStatefulServices == false {
                        Text("Nothing to back up yet: the project declares no database, bucket or volume. The schedule stays and snapshots start once a deploy adds one.")
                    } else if !enabled {
                        Text("Only manual snapshots; existing scheduled ones stay and no longer expire.")
                    }
                }

                if enabled {
                    Section {
                        Picker("Repeat", selection: $draft.repeatEvery) {
                            ForEach(ScheduleDraft.Repeat.allCases) { option in
                                Text(option.label).tag(option)
                            }
                        }
                        .pickerStyle(.menu)
                        timeControls
                    } footer: {
                        scheduleFooter
                    }

                    Section {
                        Picker("Keep Snapshots For", selection: $draft.retentionSeconds) {
                            ForEach(retentionChoices, id: \.self) { seconds in
                                Text(describeRetention(seconds: seconds)).tag(seconds)
                            }
                        }
                        .pickerStyle(.menu)
                    } footer: {
                        Text("Scheduled snapshots expire after this; the newest is always kept. Manual ones stay until you delete them.")
                    }
                }
            }
            .disabled(!canEdit || saving)
            .navigationTitle("Backup Schedule")
            .navigationSubtitle("\(model.project.name) · \(environment.name)")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(role: .cancel) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if saving {
                        ProgressView()
                    } else {
                        Button(role: .confirm) { save() }
                            .disabled(!canEdit || !changed || (enabled && !draft.isValid))
                    }
                }
            }
            .alert("Could Not Save", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
                Button("OK") {}
            } message: {
                Text(error ?? "")
            }
        }
        .interactiveDismissDisabled(changed)
    }

    // MARK: Controls

    @ViewBuilder private var timeControls: some View {
        switch draft.repeatEvery {
        case .custom:
            TextField("Cron expression", text: $draft.expression)
                .monospaced()
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
        case .hourly, .every6Hours, .every12Hours:
            Picker("Minute", selection: $draft.minute) {
                ForEach(minuteChoices, id: \.self) { minute in
                    Text(String(format: ":%02d", minute)).tag(minute)
                }
            }
            .pickerStyle(.menu)
        case .daily, .weekly, .monthly:
            if draft.repeatEvery == .weekly {
                Picker("Day", selection: $draft.weekday) {
                    ForEach(0..<7, id: \.self) { day in
                        Text(ScheduleDraft.weekdayNames[day]).tag(day)
                    }
                }
                .pickerStyle(.menu)
            } else if draft.repeatEvery == .monthly {
                Picker("Day of the Month", selection: $draft.dayOfMonth) {
                    ForEach(1...28, id: \.self) { day in
                        Text("\(day)").tag(day)
                    }
                }
                .pickerStyle(.menu)
            }
            LabeledContent("Time") {
                HStack(spacing: 8) {
                    DatePicker("Time", selection: time, displayedComponents: .hourAndMinute)
                        .labelsHidden()
                        .environment(\.timeZone, Self.utc)
                    Text("UTC")
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private var scheduleFooter: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Runs as \(Text(draft.cron).monospaced()) in UTC.")
            if let next = draft.nextRun(after: .now) {
                Text("Next: \(next, format: .dateTime.weekday(.wide).hour().minute()) your time.")
            } else if draft.repeatEvery == .custom {
                Text("Five fields: minute, hour, day of month, month, day of week.")
            }
        }
    }

    // MARK: Values

    private static let utc = TimeZone(identifier: "UTC")!

    /// The time of day as a date the picker shows in UTC.
    private var time: Binding<Date> {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = Self.utc
        return Binding {
            calendar.date(from: DateComponents(year: 2026, month: 1, day: 1, hour: draft.hour, minute: draft.minute)) ?? .now
        } set: { date in
            let parts = calendar.dateComponents([.hour, .minute], from: date)
            draft.hour = parts.hour ?? 0
            draft.minute = parts.minute ?? 0
        }
    }

    private var minuteChoices: [Int] {
        Array(Set([0, 15, 30, 45, draft.minute])).sorted()
    }

    /// The offered retentions, with the current one when it is another.
    private var retentionChoices: [Int64] {
        Array(Set(ScheduleDraft.retentionChoices + [draft.retentionSeconds])).sorted()
    }

    private var result: BackupSchedule? {
        enabled ? draft.schedule : nil
    }

    private var changed: Bool {
        result != environment.settings?.backup
    }

    private func save() {
        saving = true
        Task {
            defer { saving = false }
            do {
                try await model.setBackupSchedule(result, for: environment)
                dismiss()
            } catch is CancellationError {
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// The environment's backup and restore runs; each opens to its steps.
struct BackupActivitySheet: View {
    let environment: ProjectEnvironment
    let model: ProjectModel

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            Group {
                if model.runs == nil {
                    ProgressView()
                } else if runs.isEmpty {
                    ContentUnavailableView(
                        "No Backup Runs Yet", systemImage: "clock.arrow.circlepath",
                        description: Text("Snapshots and restores of \(environment.name) appear here."))
                } else {
                    List {
                        ForEach(runs.groupedByDay(), id: \.day) { group in
                            Section {
                                ForEach(group.runs) { run in
                                    NavigationLink(value: RunSheetItem(id: run.id)) {
                                        RunRow(run: run, directory: model.directory)
                                    }
                                }
                            } header: {
                                Text(group.day, format: .dateTime.month(.abbreviated).day().year())
                            }
                        }
                    }
                }
            }
            .navigationTitle("Activity")
            .largeSubtitle("backup and restore runs · \(environment.name)", monospaced: true)
            .navigationDestination(for: RunSheetItem.self) { RunDetailView(runID: $0.id, model: model) }
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(role: .close) { dismiss() }
                }
            }
        }
    }

    private var runs: [Run] {
        (model.runs ?? []).filter(RunFilter.backups.includes)
    }
}
