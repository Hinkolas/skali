import SkaliKit
import SwiftUI

/// The object store at a glance, as the Studio's Object storage page: how
/// many more buckets fit, the volume size that decides it, the volume
/// servers, and every bucket by what it stores. Refreshes every 30
/// seconds on screen.
struct ObjectStorageView: View {
    @Environment(ClusterSession.self) private var session
    @State private var showingBuckets = false

    /// The buckets the screen lists before "Show All Buckets".
    private let bucketPreview = 5

    var body: some View {
        List {
            if let store = session.system.objectStore.store {
                Section {
                    ResourceHeader(
                        systemImage: ServiceKind.bucket.systemImage, tint: store.headroomColor, name: "Object Storage",
                        status: StatusCapsule(text: store.summary.text, tint: store.summary.tint),
                        detail: store.managed ? "replication \(store.replication)" : "local all-in-one")
                }
                .listRowBackground(Color.clear)

                if store.headroom == .exhausted || store.headroom == .low {
                    HeadroomNotice(store: store)
                }

                Section {
                    tiles(store)
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())

                volumeSizeSection(store)
                bucketsSection
                if let inventory = session.objectStoreBuckets, !inventory.unclaimed.isEmpty {
                    leftoversSection(inventory.unclaimed)
                }
                serversSection(store)
                storeSection(store)
            }
        }
        .overlay {
            switch session.system.objectStore {
            case .unknown:
                ProgressView()
            case .none:
                ContentUnavailableView(
                    "No Object Store", systemImage: ServiceKind.bucket.systemImage,
                    description: Text("The store comes up once a node carries the object-storage capability."))
            case .unavailable:
                ContentUnavailableView(
                    "Object Store Unavailable", systemImage: "exclamationmark.triangle",
                    description: Text("The cluster did not answer. Pull to try again."))
            case .set:
                EmptyView()
            }
        }
        .navigationTitle("Object Storage")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            while !Task.isCancelled {
                await session.loadObjectStorage()
                try? await Task.sleep(for: .seconds(30))
            }
        }
        .refreshable { await session.loadObjectStorage() }
        .sheet(isPresented: $showingBuckets) {
            if let inventory = session.objectStoreBuckets {
                NavigationStack { ObjectStoreBucketsView(inventory: inventory) }
                    .presentationDetents([.medium, .large])
                    .presentationDragIndicator(.visible)
            }
        }
    }

    // MARK: Now

    private func tiles(_ store: ObjectStore) -> some View {
        let capacity = store.capacity
        let limit = capacity.map { store.buckets + $0.bucketHeadroom }
        return VStack(spacing: 10) {
            UsageTileRow {
                UsageTile(
                    title: "Buckets", value: "\(store.buckets)",
                    fraction: limit.map { $0 > 0 ? Double(store.buckets) / Double($0) : 1 },
                    color: store.headroomColor,
                    caption: capacity.map { store.headroom == .exhausted ? "no room for more" : "~\($0.bucketHeadroom) more fit" }
                        ?? "capacity unknown")
                UsageTile(
                    title: "Volume Slots", value: capacity.map { "\($0.usedSlots)" } ?? "—",
                    fraction: capacity.map { $0.slots > 0 ? Double($0.usedSlots) / Double($0.slots) : 1 },
                    color: store.headroomColor,
                    caption: capacity.map { "of \($0.slots) · \($0.freeSlots) free" } ?? "not read")
            }
            UsageTileRow {
                UsageTile(
                    title: "Stored", value: session.objectStoreBuckets.map { formatBytes($0.storedBytes) } ?? "—",
                    fraction: nil, color: ServiceKind.bucket.tint,
                    caption: capacity.map { "×\($0.copies) copies" } ?? "once per object",
                    showsBar: false)
                UsageTile(
                    title: "Volume Size", value: formatVolumeSize(store.volumeSize.limitMb),
                    fraction: nil, color: ServiceKind.bucket.tint,
                    caption: store.volumeSize.default ? "platform default" : "set by an admin",
                    showsBar: false)
            }
        }
    }

    // MARK: Volume size

    private func volumeSizeSection(_ store: ObjectStore) -> some View {
        Section {
            NavigationLink(value: SettingsRoute.volumeSize) {
                LabeledContent("Volume Size") {
                    Text(formatVolumeSize(store.volumeSize.limitMb) + (store.volumeSize.default ? " · default" : ""))
                        .monospaced()
                }
            }
            if let perBucket = store.slotsPerBucket {
                LabeledContent("Slots per Bucket", value: "\(perBucket)")
            }
        } footer: {
            Text("Every bucket keeps volumes of its own, and each volume copy takes a slot. Servers offer their free disk divided by the volume size as slots, so a smaller size fits more buckets.")
        }
    }

    // MARK: Buckets

    private var bucketsSection: some View {
        let inventory = session.objectStoreBuckets
        let buckets = inventory?.bucketsBySize ?? []
        let total = inventory?.buckets.reduce(Int64(0)) { $0 + ($1.usage?.usedBytes ?? 0) } ?? 0
        return Section {
            if inventory == nil {
                Text("The bucket list could not be loaded.").foregroundStyle(.secondary)
            } else if buckets.isEmpty {
                Text("The first declared bucket lands here.").foregroundStyle(.secondary)
            }
            ForEach(buckets.prefix(bucketPreview)) { bucket in
                ObjectStoreBucketRow(bucket: bucket, totalBytes: total)
            }
            if buckets.count > bucketPreview {
                Button("Show All \(buckets.count) Buckets") { showingBuckets = true }
            }
        } header: {
            Text("Buckets")
        } footer: {
            if let inventory, !buckets.isEmpty {
                Text(inventory.usageObserved
                    ? "The largest by stored bytes, counted once whatever the replication."
                    : "Usage unknown: the master did not answer.")
            }
        }
    }

    private func leftoversSection(_ collections: [ObjectStoreBuckets.Collection]) -> some View {
        Section {
            ForEach(collections) { collection in
                LabeledContent {
                    Text(collection.usage.slots == 1 ? "1 slot" : "\(collection.usage.slots) slots")
                        .monospacedDigit()
                } label: {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(collection.collection).font(.subheadline.monospaced()).lineLimit(1)
                        Text(formatBytes(collection.usage.usedBytes)).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
        } header: {
            Text("Leftover Collections")
        } footer: {
            Text("No live bucket claims these: left behind by a deletion that did not finish, or created outside Skali. They hold their slots until removed in the SeaweedFS shell, which deletes their data for good.")
        }
    }

    // MARK: Servers

    private func serversSection(_ store: ObjectStore) -> some View {
        Section {
            if let capacity = store.capacity {
                ForEach(capacity.servers) { server in
                    VStack(alignment: .leading, spacing: 6) {
                        HStack {
                            Text(server.node ?? server.url).font(.subheadline.monospaced()).lineLimit(1)
                            Spacer()
                            Text("\(server.usedSlots) / \(server.slots) · \(server.freeSlots) free")
                                .font(.caption.monospacedDigit())
                                .foregroundStyle(.secondary)
                        }
                        UsageBar(
                            fraction: server.slots > 0 ? Double(server.usedSlots) / Double(server.slots) : 1,
                            color: server.freeSlots == 0 ? .red : ServiceKind.bucket.tint)
                    }
                    .padding(.vertical, 2)
                    .accessibilityElement(children: .combine)
                }
            } else {
                Text("The master did not answer.").foregroundStyle(.secondary)
            }
        } header: {
            Text("Volume Servers")
        } footer: {
            if let capacity = store.capacity {
                Text(capacity.copies == 1
                    ? "One copy of every volume. Slots follow each server's free disk, so they shift as data is written."
                    : "\(capacity.copies) copies of every volume, on different servers. Slots follow each server's free disk, so they shift as data is written.")
            }
        }
    }

    // MARK: Setup

    private func storeSection(_ store: ObjectStore) -> some View {
        Section("Store") {
            LabeledContent("Engine") { Text(store.engineVersion.map { "SeaweedFS \($0)" } ?? "SeaweedFS").monospaced() }
            if store.managed {
                LabeledContent("Masters", value: mastersText(store))
                LabeledContent("Volume Servers", value: serversText(store))
            } else {
                LabeledContent("Shape", value: "Local all-in-one")
            }
            LabeledContent("Replication") { Text(store.replication).monospaced() }
            if let observed = store.observed, observed.underReplicatedVolumes > 0 {
                LabeledContent("Short of a Copy", value: "\(observed.underReplicatedVolumes) volumes")
            }
            LabeledContent("Created", value: store.createdAt, format: .dateTime.day().month().year())
        }
    }

    private func mastersText(_ store: ObjectStore) -> String {
        guard let observed = store.observed else { return "\(store.masters) · not observed" }
        return "\(observed.mastersReady) of \(store.masters) ready"
    }

    private func serversText(_ store: ObjectStore) -> String {
        guard let observed = store.observed else { return "\(store.volumeServers) · not observed" }
        return "\(observed.volumeServersReady) of \(store.volumeServers) serving"
    }
}

/// A store out of room or close to it: what fails, and what helps.
private struct HeadroomNotice: View {
    let store: ObjectStore

    var body: some View {
        Section {
            Label {
                VStack(alignment: .leading, spacing: 4) {
                    Text(store.headroom == .exhausted ? "No Room for Another Bucket" : "Close to the Bucket Limit")
                        .font(.headline)
                    Text(message).font(.subheadline).foregroundStyle(.secondary)
                }
            } icon: {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(store.headroom == .exhausted ? .red : .orange)
            }
        }
    }

    private var message: String {
        let first = store.headroom == .exhausted
            ? "A new bucket accepts reads but refuses every write, and buckets whose volumes are full stop taking writes too."
            : "About \(store.capacity?.bucketHeadroom ?? 0) more buckets fit before new ones refuse writes."
        return first + " Lower the volume size (now \(formatVolumeSize(store.volumeSize.limitMb))), free the slots of leftover collections, or add disk to the volume servers."
    }
}

/// Every bucket, as a sheet: searchable, largest first, the projects'
/// buckets apart from the platform's own.
struct ObjectStoreBucketsView: View {
    let inventory: ObjectStoreBuckets

    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    var body: some View {
        let buckets = filtered
        let projects = buckets.filter { !$0.isSystem }
        let system = buckets.filter(\.isSystem)
        let total = inventory.buckets.reduce(Int64(0)) { $0 + ($1.usage?.usedBytes ?? 0) }
        List {
            if !projects.isEmpty {
                Section("Projects · \(projects.count)") {
                    ForEach(projects) { ObjectStoreBucketRow(bucket: $0, totalBytes: total) }
                }
            }
            if !system.isEmpty {
                Section {
                    ForEach(system) { ObjectStoreBucketRow(bucket: $0, totalBytes: total) }
                } header: {
                    Text("System · \(system.count)")
                } footer: {
                    Text("The platform's own buckets.")
                }
            }
        }
        .overlay {
            if buckets.isEmpty && !query.isEmpty {
                ContentUnavailableView.search(text: query)
            }
        }
        .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: "Buckets or projects")
        .navigationTitle("Buckets")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") { dismiss() }
            }
        }
    }

    private var filtered: [ObjectStoreBuckets.Bucket] {
        let buckets = inventory.bucketsBySize
        guard !query.isEmpty else { return buckets }
        return buckets.filter {
            $0.bucketName.localizedStandardContains(query) || $0.ownerLabel.localizedStandardContains(query)
        }
    }
}

#Preview {
    PreviewHost { ObjectStorageView() }
}

#Preview("Out of room") {
    PreviewHost(demo: DemoCluster(objectStorage: .full)) { ObjectStorageView() }
}
