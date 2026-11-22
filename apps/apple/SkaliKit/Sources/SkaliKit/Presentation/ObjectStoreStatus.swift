import Foundation

// How the object store reads at a glance, and what another volume size
// limit would do to it: the same rules as the Studio's object storage page
// (studio/src/lib/types/objectstorage.ts).

/// How close the store is to refusing new buckets.
public enum ObjectStoreHeadroom: Hashable, Sendable {
    /// No new bucket fits: its first write would fail.
    case exhausted
    /// Under five buckets left, or under a tenth of what the store holds in all.
    case low
    case ok
    /// The master did not answer.
    case unknown
}

/// What a volume size limit would leave: free slots and buckets that fit.
public struct ObjectStoreEstimate: Hashable, Sendable {
    public let freeSlots: Int
    public let bucketHeadroom: Int
    /// A server's free disk was unknown, so its slots were scaled instead
    /// of computed; the room its volumes reserve is left out.
    public let rough: Bool
}

extension ObjectStore {
    public var headroom: ObjectStoreHeadroom {
        guard let capacity else { return .unknown }
        if capacity.volumeHeadroom == 0 || capacity.bucketHeadroom == 0 { return .exhausted }
        let total = buckets + capacity.bucketHeadroom
        if capacity.bucketHeadroom < 5 || Double(capacity.bucketHeadroom) < Double(total) * 0.1 {
            return .low
        }
        return .ok
    }

    /// Serving with less than its recorded shape.
    public var isDegraded: Bool {
        guard let observed else { return false }
        return observed.mastersReady < masters
            || observed.volumeServersReady < volumeServers
            || !observed.filerReady
            || observed.underReplicatedVolumes > 0
            || observed.replicationPendingVolumes > 0
    }

    /// The fresh observation says the S3 gateway is down.
    public var s3Down: Bool {
        guard let observed, observed.state == "fresh" else { return false }
        return !observed.s3Ready
    }

    /// The version from the image reference: "4.48".
    public var engineVersion: String? {
        guard let tag = image.split(separator: ":").last, image.contains(":") else { return nil }
        return String(tag)
    }

    /// Slots every new bucket takes: volumes it grows times their copies.
    public var slotsPerBucket: Int? {
        capacity.map { $0.volumesPerBucket * $0.copies }
    }

    /// The free slots and bucket headroom under another limit, server by
    /// server: a volume server offers its volumes plus (free disk - the
    /// room its writable volumes may still grow into) / limit, at least
    /// one. A server's free disk is its node's sample, else recovered from
    /// its current free slots (exact to within one volume); without either
    /// its free slots are scaled by current/next and the estimate is rough.
    public func estimate(volumeSizeMb next: Int) -> ObjectStoreEstimate? {
        guard let capacity, next > 0 else { return nil }
        let current = volumeSize.limitMb
        if next == current {
            return ObjectStoreEstimate(freeSlots: capacity.freeSlots, bucketHeadroom: capacity.bucketHeadroom, rough: false)
        }
        var rough = false
        let free = capacity.servers.map { server in
            if let exact = server.freeSlots(currentMb: current, nextMb: next) { return exact }
            rough = true
            return server.freeSlots * current / next
        }
        return ObjectStoreEstimate(
            freeSlots: free.reduce(0, +),
            bucketHeadroom: volumeHeadroom(free: free, copies: max(1, capacity.copies)) / max(1, capacity.volumesPerBucket),
            rough: rough)
    }
}

private let megabyte: Int64 = 1024 * 1024

extension ObjectStore.Server {
    /// The room the writable volumes may still grow into under a limit.
    func reservedBytes(limitMb: Int) -> Int64 {
        let limit = Int64(limitMb) * megabyte
        return (writableVolumeBytes ?? []).reduce(0) { $0 + max(0, limit - $1) }
    }

    func freeSlots(currentMb: Int, nextMb: Int) -> Int? {
        guard writableVolumeBytes != nil else { return nil }
        let freeDisk: Int64
        if let sampled = freeDiskBytes {
            freeDisk = sampled
        } else if slots > usedSlots {
            freeDisk = Int64(slots - usedSlots) * Int64(currentMb) * megabyte + reservedBytes(limitMb: currentMb)
        } else {
            return nil
        }
        let unclaimed = freeDisk - reservedBytes(limitMb: nextMb)
        let grown = unclaimed > 0 ? Int(unclaimed / (Int64(nextMb) * megabyte)) : 0
        return max(0, max(1, usedSlots + grown) - usedSlots)
    }
}

/// The largest n with sum(min(free_i, n)) >= copies * n: how many volumes
/// can grow with every copy on a different server.
public func volumeHeadroom(free: [Int], copies: Int) -> Int {
    let open = free.filter { $0 > 0 }
    guard open.count >= copies, copies > 0 else { return 0 }
    var low = 0
    var high = open.reduce(0, +) / copies
    while low < high {
        let n = (low + high + 1) / 2
        if open.reduce(0, { $0 + min($1, n) }) >= copies * n {
            low = n
        } else {
            high = n - 1
        }
    }
    return low
}

/// "256 MB", "1 GB", "1.5 GB": the engine's megabytes, as an admin reads them.
public func formatVolumeSize(_ megabytes: Int) -> String {
    if megabytes >= 1000 && megabytes % 1000 == 0 { return "\(megabytes / 1000) GB" }
    if megabytes >= 1000 {
        return String(format: "%.1f GB", Double(megabytes) / 1000).replacingOccurrences(of: ".0 GB", with: " GB")
    }
    return "\(megabytes) MB"
}

/// Parses a volume size: whole megabytes, or a figure with MB or GB.
public func parseVolumeSize(_ input: String) -> Int? {
    let text = input.trimmingCharacters(in: .whitespaces).lowercased()
    var number = text
    var scale = 1.0
    if text.hasSuffix("gb") {
        number = String(text.dropLast(2))
        scale = 1000
    } else if text.hasSuffix("mb") {
        number = String(text.dropLast(2))
    }
    number = number.trimmingCharacters(in: .whitespaces)
    guard !number.isEmpty, number.allSatisfy({ $0.isNumber || $0 == "." }), let value = Double(number) else {
        return nil
    }
    let megabytes = value * scale
    guard megabytes > 0, megabytes == megabytes.rounded() else { return nil }
    return Int(megabytes)
}
