import SkaliKit
import SwiftUI

/// A bucket: what it holds against its quota, how it is set up, where it
/// answers when public, who uses it, and how it is backed up.
struct BucketServiceView: View {
    let key: String
    let spec: BucketSpec
    let model: ProjectModel

    var body: some View {
        EnvironmentGate(model: model) { environment in
            List {
                Section {
                    ServiceHeaderView(
                        kind: .bucket, name: key, state: model.liveState(.bucket, key),
                        detail: "bucket · \(spec.visibility)",
                        dnsPending: (model.liveStatus(.bucket, key)?.deferredRoutes ?? 0) > 0)
                }
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 0, leading: 4, bottom: 8, trailing: 4))

                Section {
                    let sample = model.storage(of: .bucket, key).first
                    CapacityCard(
                        title: "Usage", used: sample?.usedBytes, capacity: spec.storageQuotaBytes,
                        capacityWord: "quota", tint: .orange)
                }

                Section("Bucket") {
                    LabeledContent("Visibility") { Text(spec.visibility).monospaced() }
                    LabeledContent("Versioning") { Text(spec.versioning).monospaced() }
                    LabeledContent("Quota") {
                        Text(spec.storageQuotaBytes.map(formatBytes) ?? "none").monospaced()
                    }
                }

                RoutesSection(routes: model.liveStatus(.bucket, key)?.routes ?? [])

                let dependents = model.definition?.dependents(of: .bucket, key) ?? []
                if !dependents.isEmpty {
                    Section("Connected Services") {
                        ForEach(dependents, id: \.self) { application in
                            NavigationLink(value: ProjectRoute.service(.application, application)) {
                                ConnectedServiceRow(kind: .application, name: application, note: "s3 keys injected")
                            }
                        }
                    }
                }

                Section {
                    ServiceBackupsRow(environment: environment, runs: model.runs)
                }
            }
        }
        .statefulServiceMenu(model, key: key)
    }
}
