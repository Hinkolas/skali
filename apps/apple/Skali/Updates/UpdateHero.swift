import SkaliKit
import SwiftUI

/// The top of the update screen: one glyph, the release, one sentence.
struct UpdateHero: View {
    let status: UpdateStatus

    @ScaledMetric(relativeTo: .largeTitle) private var tile: CGFloat = 76

    var body: some View {
        VStack(spacing: 6) {
            glyph
                .padding(.bottom, 12)
            Text(title)
                .font(.title2.weight(.bold))
                .multilineTextAlignment(.center)
            Text(message)
                .font(.body)
                .multilineTextAlignment(.center)
            if let caption {
                Text(caption)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 8)
        .accessibilityElement(children: .combine)
    }

    private var glyph: some View {
        let state = status.summary.state
        let tint: Color = switch state {
        case .failed: .red
        case .incomplete: .orange
        default: .accentColor
        }
        let symbol = switch state {
        case .failed, .incomplete: "exclamationmark.triangle.fill"
        case .updating: "arrow.triangle.2.circlepath"
        case .available: "arrow.down"
        default: "server.rack"
        }
        return Image(systemName: symbol)
            .font(.system(size: tile * 0.42, weight: .semibold))
            .foregroundStyle(.white)
            .symbolEffect(.rotate.byLayer, options: .repeat(.continuous), isActive: state == .updating)
            .contentTransition(.symbolEffect(.replace))
            .frame(width: tile, height: tile)
            .background(tint.gradient, in: .rect(cornerRadius: tile * 0.24))
            .shadow(color: tint.opacity(0.45), radius: 18, y: 6)
            .accessibilityHidden(true)
    }

    private var title: String {
        let summary = status.summary
        switch summary.state {
        case .available, .updating:
            return "skali \(displayVersion(summary.targetVersion ?? status.latest?.version ?? ""))"
        case .failed:
            return "Update Needs Attention"
        default:
            return "skali \(displayVersion(status.clusterVersion))"
        }
    }

    private var message: String {
        let summary = status.summary
        switch summary.state {
        case .current: return "Your cluster is up to date."
        case .available: return "A new release is available."
        case .updating: return "Updating your cluster…"
        case .failed:
            return "The update to \(displayVersion(summary.targetVersion ?? "the new release")) did not finish."
        case .incomplete: return "The update is incomplete."
        case .notChecked: return "Not checked for updates yet."
        case .noRelease: return "Nothing is published on the \(status.channel.label.lowercased()) channel yet."
        case .unknown: return summary.detail ?? "Update status is unavailable."
        }
    }

    private var caption: String? {
        let summary = status.summary
        switch summary.state {
        case .available:
            var parts = ["You're on \(displayVersion(status.clusterVersion))"]
            if status.latest?.prerelease == true { parts.append("prerelease") }
            return parts.joined(separator: " · ")
        case .updating:
            guard let operation = status.operation else { return "Starting" }
            return "Started \(operation.startedAt.formatted(.relative(presentation: .named)))"
        case .failed:
            guard let operation = status.operation else { return nil }
            return "Stopped \(operation.updatedAt.formatted(.relative(presentation: .named)))"
        case .incomplete:
            return "Components disagree on the release. Finishing brings every node to \(displayVersion(summary.targetVersion ?? "it"))."
        case .unknown:
            return nil
        default:
            guard let checked = status.lastCheckedAt else { return "Never checked" }
            return "Last checked \(checked.formatted(.relative(presentation: .named)))"
        }
    }
}

/// The last release check failed: why, worded by kind as the Studio does.
struct FeedErrorNotice: View {
    let kind: String?
    let detail: String

    var body: some View {
        Label {
            VStack(alignment: .leading, spacing: 4) {
                Text(title).font(.headline)
                Text(hint).font(.subheadline).foregroundStyle(.secondary)
            }
        } icon: {
            Image(systemName: kind == "offline" || kind == "unavailable" ? "icloud.slash" : "exclamationmark.triangle")
                .foregroundStyle(.orange)
        }
        .accessibilityHint(detail)
    }

    private var title: String {
        switch kind {
        case "offline": "Update Servers Are Offline"
        case "not_found": "Release Feed Not Found"
        case "rate_limited": "Release Feed Is Rate Limiting"
        case "unavailable": "Update Servers Are Unavailable"
        case "invalid": "Unexpected Answer from the Release Feed"
        default: "Could Not Check for Updates"
        }
    }

    private var hint: String {
        switch kind {
        case "offline": "skalid could not reach the release feed. GitHub may be down, or the cluster has no outbound HTTPS. It retries every hour."
        case "not_found": "The release feed answered 404. The repository may be private or renamed, or the feed URL is wrong. It retries every hour."
        case "rate_limited": "The feed refused the request for now. The next scheduled check usually succeeds."
        case "unavailable": "The release feed answered with a server error. It retries every hour."
        case "invalid": "The feed answered, but not with a release listing. Check the configured feed URL."
        default: detail
        }
    }
}

/// What an available release changes, from what the nodes report today.
struct ReleaseChangesSection: View {
    let status: UpdateStatus
    let release: Release

    var body: some View {
        let controllers = status.nodes.count(where: \.isController)
        let workers = status.nodes.count - controllers
        Section {
            ChangeRow(
                title: "Platform", detail: "skalid, Studio and host agents",
                from: status.clusterVersion, to: release.version)
            if status.latestChangesKubernetes, let current = status.commonKubernetesVersion, let pin = release.k3s {
                ChangeRow(title: "Kubernetes", detail: "Each node drains and rejoins", from: current, to: pin)
            } else {
                LabeledContent {
                    Text(status.commonKubernetesVersion ?? release.k3s ?? "not reported").monospaced()
                } label: {
                    Text("Kubernetes")
                    Text(release.k3s == nil ? "Pin not published" : "Unchanged by this release")
                }
            }
            LabeledContent {
                Text("\(status.nodes.count)").monospacedDigit()
            } label: {
                Text("Nodes")
                Text("\(controllers) \(controllers == 1 ? "controller" : "controllers") first, then \(workers) \(workers == 1 ? "worker" : "workers")")
            }
            if let url = release.url.flatMap(URL.init(string:)) {
                Link(destination: url) {
                    Label("Release Notes", systemImage: "doc.text")
                }
            }
        } header: {
            Text("What Changes")
        } footer: {
            if let published = release.publishedAt {
                Text("Published \(published.formatted(date: .abbreviated, time: .omitted)) on the \(status.channel.label.lowercased()) channel.")
            }
        }
    }
}

private struct ChangeRow: View {
    let title: String
    let detail: String
    let from: String
    let to: String

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title)
            HStack(spacing: 6) {
                Text(from).foregroundStyle(.secondary)
                Image(systemName: "arrow.right").font(.caption.weight(.semibold)).foregroundStyle(.tertiary)
                Text(to).foregroundStyle(.tint)
            }
            .font(.callout.monospaced())
            Text(detail).font(.subheadline).foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
    }
}
