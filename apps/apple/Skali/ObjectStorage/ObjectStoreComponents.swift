import SkaliKit
import SwiftUI

extension ObjectStore {
    /// The Settings row's value and the header's pill: an outage first,
    /// then how many buckets still fit.
    var summary: (text: String, tint: Color) {
        if s3Down { return ("S3 Not Serving", .red) }
        switch headroom {
        case .exhausted: return ("No Room for Buckets", .red)
        case .low: return ("\(capacity?.bucketHeadroom ?? 0) Buckets Left", .orange)
        case .unknown: return (bucketCount, .secondary)
        case .ok: break
        }
        if observed?.state == "fresh" && isDegraded { return ("Degraded", .orange) }
        return (bucketCount, .green)
    }

    var bucketCount: String {
        buckets == 1 ? "1 bucket" : "\(buckets) buckets"
    }

    /// The bars' color: amber when few buckets are left, red when none.
    var headroomColor: Color {
        switch headroom {
        case .exhausted: .red
        case .low: .orange
        case .ok, .unknown: ServiceKind.bucket.tint
        }
    }

    /// "SeaweedFS 4.48 · 3 masters · 6 volume servers · replication 001".
    var facts: String {
        var parts = [engineVersion.map { "SeaweedFS \($0)" } ?? "SeaweedFS"]
        if managed {
            parts.append(masters == 1 ? "1 master" : "\(masters) masters")
            parts.append(volumeServers == 1 ? "1 volume server" : "\(volumeServers) volume servers")
        } else {
            parts.append("local all-in-one")
        }
        parts.append("replication \(replication)")
        return parts.joined(separator: " · ")
    }

    /// What saving a new size restarts.
    var restartNote: String {
        if !managed { return "The local store restarts; buckets are briefly unavailable." }
        return masters < 2
            ? "The master restarts; writes pause briefly."
            : "The masters restart one at a time."
    }
}

/// One bucket: its name, whose it is, what it stores and the slots it holds.
struct ObjectStoreBucketRow: View {
    let bucket: ObjectStoreBuckets.Bucket
    /// Every bucket's stored bytes, for the share bar; zero hides it.
    var totalBytes: Int64 = 0

    @ScaledMetric(relativeTo: .body) private var tile: CGFloat = 30

    var body: some View {
        HStack(spacing: 12) {
            ResourceTile(
                systemImage: bucket.isSystem ? "gearshape" : ServiceKind.bucket.systemImage,
                tint: bucket.isSystem ? .secondary : ServiceKind.bucket.tint, size: tile)
            VStack(alignment: .leading, spacing: 3) {
                Text(bucket.bucketName)
                    .font(.subheadline.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(bucket.ownerLabel)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                if let share, totalBytes > 0 {
                    UsageBar(fraction: share, color: ServiceKind.bucket.tint, height: 3)
                        .padding(.top, 2)
                }
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                Text(bucket.usage.map { formatBytes($0.usedBytes) } ?? "—")
                    .font(.subheadline.monospacedDigit())
                Text(slotsText)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private var share: Double? {
        guard let used = bucket.usage?.usedBytes, totalBytes > 0 else { return nil }
        return Double(used) / Double(totalBytes)
    }

    private var slotsText: String {
        guard let usage = bucket.usage else { return "usage unknown" }
        if usage.slots == 0 { return "no volume yet" }
        return usage.slots == 1 ? "1 slot" : "\(usage.slots) slots"
    }
}
