import SkaliKit
import SwiftUI

// The Studio's service and status colors (studio/src/routes/layout.css),
// expressed as system colors so they adapt to light mode, Increase
// Contrast and the rest of the accessibility settings.

extension ServiceKind {
    var tint: Color {
        switch self {
        case .application: .serviceApplication
        case .database: .cyan
        case .bucket: .orange
        }
    }

    var systemImage: String {
        switch self {
        case .application: "shippingbox"
        case .database: "cylinder.split.1x2"
        case .bucket: "externaldrive"
        }
    }

    var label: String {
        switch self {
        case .application: "Application"
        case .database: "Database"
        case .bucket: "Bucket"
        }
    }

    var pluralLabel: String {
        switch self {
        case .application: "applications"
        case .database: "databases"
        case .bucket: "buckets"
        }
    }
}

extension ServiceHealth {
    var tint: Color {
        switch self {
        case .healthy: .green
        case .progressing, .degraded: .orange
        case .unhealthy: .red
        case .unknown: .secondary
        }
    }

    var label: String {
        switch self {
        case .healthy: "Healthy"
        case .progressing: "Progressing"
        case .degraded: "Degraded"
        case .unhealthy: "Unhealthy"
        case .unknown: "Unknown"
        }
    }
}

extension ServiceCounts {
    func count(of kind: ServiceKind) -> Int {
        switch kind {
        case .application: applications
        case .database: databases
        case .bucket: buckets
        }
    }
}
