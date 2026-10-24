import Foundation

/// Where a cluster lives: the API base the client talks to and the Studio
/// origin the browser sign-in opens. Mirrors the CLI's `skali remote add`
/// resolution (cmd/skali/remote.go, masterCandidates): a bare hostname
/// becomes `https://<host>/api`, an explicit URL is used as given.
public struct ClusterAddress: Hashable, Codable, Sendable {
    /// The API base, e.g. `https://skali.example.com/api`.
    public let apiURL: URL

    public init(apiURL: URL) {
        self.apiURL = apiURL
    }

    /// Parses what someone types into the sign-in field. Plain http is only
    /// accepted when spelled out, and App Transport Security still decides
    /// whether the app may reach it.
    public init?(userInput raw: String) {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        let explicit = trimmed.contains("://")
        guard var components = URLComponents(string: explicit ? trimmed : "https://" + trimmed),
              let scheme = components.scheme?.lowercased(), scheme == "https" || scheme == "http",
              let host = components.host, !host.isEmpty,
              components.user == nil, components.password == nil,
              components.query == nil, components.fragment == nil
        else { return nil }
        var path = components.path
        while path.hasSuffix("/") { path.removeLast() }
        if path.isEmpty && !explicit { path = "/api" }
        components.scheme = scheme
        components.host = host.lowercased()
        components.path = path
        guard let url = components.url else { return nil }
        self.apiURL = url
    }

    /// The host as people know the cluster, e.g. `skali.example.com`.
    public var host: String {
        apiURL.host() ?? apiURL.absoluteString
    }

    /// The Studio origin: the API base without its trailing `/api`.
    public var studioURL: URL {
        var components = URLComponents(url: apiURL, resolvingAgainstBaseURL: false)!
        var path = components.path
        if path.hasSuffix("/api") { path.removeLast(4) }
        components.path = path
        return components.url!
    }

    /// The Studio page that approves a device request.
    public func verificationURL(userCode: String) -> URL {
        studioURL
            .appending(path: "auth/device")
            .appending(queryItems: [URLQueryItem(name: "code", value: userCode)])
    }
}
