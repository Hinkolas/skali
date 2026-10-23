import Foundation

/// `GET /v1/environments/{id}/metrics` (`EnvironmentMetrics`): usage per
/// application on a fixed step grid. Every series shares `timestamps`; nil
/// marks a bucket without samples, and the trailing bucket is the one in
/// progress.
public struct EnvironmentMetrics: Codable, Hashable, Sendable {
    public let window: NodeMetrics.Window
    public let stepSeconds: Int
    public let timestamps: [Date]
    public let applications: [ApplicationSeries]

    public init(window: NodeMetrics.Window, stepSeconds: Int, timestamps: [Date], applications: [ApplicationSeries]) {
        self.window = window
        self.stepSeconds = stepSeconds
        self.timestamps = timestamps
        self.applications = applications
    }
}

public struct ApplicationSeries: Codable, Hashable, Sendable {
    public let key: String
    public let cpuMillicores: [Int?]
    public let memoryBytes: [Int64?]
    /// Traffic through the platform edge; absent until edge samples exist.
    public let edge: EdgeSeries?

    public init(key: String, cpuMillicores: [Int?], memoryBytes: [Int64?], edge: EdgeSeries? = nil) {
        self.key = key
        self.cpuMillicores = cpuMillicores
        self.memoryBytes = memoryBytes
        self.edge = edge
    }
}

/// Per bucket: the requests, and the bytes in each direction, that arrived
/// within it.
public struct EdgeSeries: Codable, Hashable, Sendable {
    public let requests: [Int64?]
    public let requestBytes: [Int64?]
    public let responseBytes: [Int64?]

    public init(requests: [Int64?], requestBytes: [Int64?], responseBytes: [Int64?]) {
        self.requests = requests
        self.requestBytes = requestBytes
        self.responseBytes = responseBytes
    }
}

/// One service's newest storage footprint (`GET /v1/projects/{id}/storage`).
public struct ServiceStorage: Codable, Hashable, Sendable {
    public enum Kind: String, Codable, Hashable, Sendable, CaseIterable {
        case volume, database, bucket, temporary
    }

    public let environmentId: String
    /// `databases.<key>` and `buckets.<key>` for claims, the bare
    /// application key for volumes and temporary storage.
    public let serviceKey: String
    public let kind: Kind
    /// Nil where usage cannot be measured; the reserved size is all there is.
    public let usedBytes: Int64?
    /// The declared size, quota or limit; 0 when unknown.
    public let capacityBytes: Int64
    public let sampledAt: Date

    public init(
        environmentId: String, serviceKey: String, kind: Kind, usedBytes: Int64?, capacityBytes: Int64,
        sampledAt: Date
    ) {
        self.environmentId = environmentId
        self.serviceKey = serviceKey
        self.kind = kind
        self.usedBytes = usedBytes
        self.capacityBytes = capacityBytes
        self.sampledAt = sampledAt
    }

    /// The service's name without its collection prefix.
    public var serviceName: String {
        for prefix in ["databases.", "buckets."] where serviceKey.hasPrefix(prefix) {
            return String(serviceKey.dropFirst(prefix.count))
        }
        return serviceKey
    }

    /// Measured usage, else the reserved size.
    public var footprint: Int64 { usedBytes ?? capacityBytes }
}

struct ProjectStorageResponse: Decodable {
    let services: [ServiceStorage]
}
