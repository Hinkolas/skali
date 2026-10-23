import Foundation

/// A cluster the app is signed in to: where it lives and who is signed in.
/// The token itself is kept in the Keychain under `id`, never here.
public struct ClusterAccount: Codable, Hashable, Sendable, Identifiable {
    public let id: UUID
    public var address: ClusterAddress
    /// The installation identity (`Skali-Instance`) recorded on sign-in,
    /// which tells a reinstalled cluster apart from the one signed in to.
    public var instanceID: String?
    /// The installation name, when the operator set one.
    public var instanceName: String?
    public var user: User

    public init(
        id: UUID = UUID(), address: ClusterAddress, instanceID: String?,
        instanceName: String?, user: User
    ) {
        self.id = id
        self.address = address
        self.instanceID = instanceID
        self.instanceName = instanceName
        self.user = user
    }

    /// The name lists show: the installation name, else the host.
    public var displayName: String { instanceName ?? address.host }
}

/// Persists the account list (without tokens) and the selected account.
public struct ClusterAccountStore {
    private let defaults: UserDefaults
    private static let accountsKey = "skali.accounts"
    private static let selectedKey = "skali.selectedAccount"

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    public func load() -> (accounts: [ClusterAccount], selected: UUID?) {
        let accounts = defaults.data(forKey: Self.accountsKey)
            .flatMap { try? JSONDecoder().decode([ClusterAccount].self, from: $0) } ?? []
        let selected = defaults.string(forKey: Self.selectedKey).flatMap(UUID.init(uuidString:))
        return (accounts, selected)
    }

    public func save(accounts: [ClusterAccount], selected: UUID?) {
        if let data = try? JSONEncoder().encode(accounts) {
            defaults.set(data, forKey: Self.accountsKey)
        }
        defaults.set(selected?.uuidString, forKey: Self.selectedKey)
    }
}
