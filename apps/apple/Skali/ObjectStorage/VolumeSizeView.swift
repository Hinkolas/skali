import SkaliKit
import SwiftUI

/// The volume size limit: the default, a common size or a custom figure,
/// each with the buckets it would still fit, as the Studio's form. Saving
/// is sudo-gated and restarts the masters; the screen asks first.
struct VolumeSizeView: View {
    @Environment(ClusterSession.self) private var session
    @Environment(\.dismiss) private var dismiss

    /// What the form would save: the default, or a size in MB.
    private enum Choice: Hashable {
        case `default`
        case size(Int)
    }

    @State private var choice: Choice?
    @State private var custom = ""
    @State private var confirming = false
    @State private var saving = false
    @State private var error: String?
    @FocusState private var customFocused: Bool

    private static let presets = [128, 256, 512, 1000, 2000, 4000, 8000]

    var body: some View {
        List {
            if let store = session.system.objectStore.store {
                sizesSection(store)
                customSection(store)
            }
        }
        .overlay {
            if session.system.objectStore.store == nil { ProgressView() }
        }
        .navigationTitle("Volume Size")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                if saving {
                    ProgressView()
                } else {
                    Button("Save") { confirming = true }
                        .disabled(!canSave)
                }
            }
        }
        .confirmationDialog(
            "Change the Volume Size?", isPresented: $confirming, titleVisibility: .visible
        ) {
            Button("Save \(target.map(formatVolumeSize) ?? "")") { save() }
        } message: {
            if let store = session.system.objectStore.store {
                Text("\(store.restartNote) Volumes already over a smaller size stay readable and stop taking writes; their buckets start new ones.")
            }
        }
        .alert("Could Not Save", isPresented: Binding { error != nil } set: { if !$0 { error = nil } }) {
            Button("OK") {}
        } message: {
            Text(error ?? "")
        }
        .disabled(saving)
        .task {
            if session.system.objectStore.store == nil { await session.loadObjectStorage() }
            seed()
        }
    }

    // MARK: Choices

    private func sizesSection(_ store: ObjectStore) -> some View {
        let size = store.volumeSize
        let sizes = Array(Set(Self.presets + [size.limitMb]))
            .filter { $0 != size.defaultMb && (size.minMb...size.maxMb).contains($0) }
            .sorted()
        return Section {
            row(store, .default, title: "Default", detail: formatVolumeSize(size.defaultMb), megabytes: size.defaultMb)
            ForEach(sizes, id: \.self) { megabytes in
                row(store, .size(megabytes), title: formatVolumeSize(megabytes), detail: nil, megabytes: megabytes)
            }
        } header: {
            Text("Size")
        } footer: {
            Text("More buckets each size would still fit, from every volume server's free disk. Existing buckets also take slots as they fill their volumes.")
        }
    }

    private func row(_ store: ObjectStore, _ option: Choice, title: String, detail: String?, megabytes: Int) -> some View {
        let estimate = store.estimate(volumeSizeMb: megabytes)
        let current = isCurrent(store, option)
        return Button {
            choice = option
            customFocused = false
        } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(title).foregroundStyle(.primary)
                        if let detail { Text(detail).monospaced().foregroundStyle(.secondary) }
                        if current { TagChip(text: "current") }
                    }
                    Text(estimateText(estimate))
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(estimate?.bucketHeadroom == 0 ? Color.red : .secondary)
                }
                Spacer()
                if choice == option {
                    Image(systemName: "checkmark").foregroundStyle(.tint).fontWeight(.semibold)
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(choice == option ? .isSelected : [])
    }

    private func customSection(_ store: ObjectStore) -> some View {
        let size = store.volumeSize
        let parsed = parseVolumeSize(custom)
        let invalid = !custom.isEmpty && !(parsed.map { (size.minMb...size.maxMb).contains($0) } ?? false)
        return Section {
            HStack {
                TextField("Size", text: $custom)
                    .keyboardType(.numberPad)
                    .monospacedDigit()
                    .focused($customFocused)
                    .onChange(of: custom) {
                        if let parsed { choice = .size(parsed) }
                    }
                Text("MB").foregroundStyle(.secondary)
            }
            if let parsed, !invalid, !Self.presets.contains(parsed), parsed != size.defaultMb {
                Text(estimateText(store.estimate(volumeSizeMb: parsed)))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        } header: {
            Text("Custom")
        } footer: {
            Text(invalid
                ? "Between \(formatVolumeSize(size.minMb)) and \(formatVolumeSize(size.maxMb)), in MB."
                : "A larger size suits a few big buckets on big disks; a smaller one fits more buckets.")
                .foregroundStyle(invalid ? .red : .secondary)
        }
    }

    private func estimateText(_ estimate: ObjectStoreEstimate?) -> String {
        guard let estimate else { return "capacity unknown" }
        let buckets = estimate.bucketHeadroom == 1 ? "1 more bucket" : "\(estimate.bucketHeadroom) more buckets"
        return "\(estimate.rough ? "roughly" : "about") \(buckets) · \(estimate.freeSlots) free slots"
    }

    // MARK: Saving

    private func isCurrent(_ store: ObjectStore, _ option: Choice) -> Bool {
        switch option {
        case .default: store.volumeSize.default
        case .size(let megabytes): !store.volumeSize.default && store.volumeSize.limitMb == megabytes
        }
    }

    /// The size the choice stands for.
    private var target: Int? {
        guard let store = session.system.objectStore.store else { return nil }
        switch choice {
        case .default: return store.volumeSize.defaultMb
        case .size(let megabytes): return megabytes
        case nil: return nil
        }
    }

    private var canSave: Bool {
        guard let store = session.system.objectStore.store, let choice, !isCurrent(store, choice) else { return false }
        if case .size(let megabytes) = choice {
            return (store.volumeSize.minMb...store.volumeSize.maxMb).contains(megabytes)
        }
        return true
    }

    private func seed() {
        guard choice == nil, let store = session.system.objectStore.store else { return }
        choice = store.volumeSize.default ? .default : .size(store.volumeSize.limitMb)
        if !store.volumeSize.default && !Self.presets.contains(store.volumeSize.limitMb) {
            custom = String(store.volumeSize.limitMb)
        }
    }

    private func save() {
        guard let choice else { return }
        let megabytes: Int? = switch choice {
        case .default: nil
        case .size(let megabytes): megabytes
        }
        saving = true
        Task {
            defer { saving = false }
            do {
                try await session.saveVolumeSize(megabytes)
                dismiss()
            } catch is CancellationError {
                // The person closed the browser sheet; nothing changed.
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

#Preview {
    PreviewHost(demo: DemoCluster(objectStorage: .full)) { VolumeSizeView() }
}
