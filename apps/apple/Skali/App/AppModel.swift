import Foundation
import Observation
import SkaliKit
import UIKit

/// The app's top-level state: the clusters signed in to, which one is
/// selected, and the live session for it. Tokens go to the Keychain; the
/// account list itself is plain preferences.
@Observable
final class AppModel {
    private(set) var accounts: [ClusterAccount] = []
    private(set) var session: ClusterSession?

    /// Launched with `-SkaliDemo`: fixtures instead of a cluster, nothing
    /// persisted. Used for previews, screenshots and trying the app out.
    let isDemo: Bool

    private let keychain = Keychain()
    private let store = ClusterAccountStore()
    private let authorizer = BrowserAuthorizer()

    init(demo: Bool = ProcessInfo.processInfo.arguments.contains("-SkaliDemo")) {
        isDemo = demo
        if demo {
            accounts = [DemoCluster.account]
            select(DemoCluster.account.id)
            return
        }
        let saved = store.load()
        accounts = saved.accounts
        select(saved.selected ?? accounts.first?.id)
    }

    var selectedAccount: ClusterAccount? { session?.account }

    // MARK: Accounts

    func select(_ id: ClusterAccount.ID?) {
        guard let account = accounts.first(where: { $0.id == id }) else {
            session = nil
            persist()
            return
        }
        guard account.id != session?.account.id else { return }
        session = ClusterSession(
            account: account, api: api(for: account),
            reauthenticate: { [weak self] in try await self?.reauthenticate(account) },
            onUnauthorized: { [weak self] in self?.forget(account) })
        persist()
    }

    /// Signs out on the cluster (best effort) and forgets the account.
    func signOut(_ account: ClusterAccount) async {
        try? await api(for: account).logout()
        forget(account)
    }

    private func forget(_ account: ClusterAccount) {
        try? keychain.removeToken(for: account.id.uuidString)
        accounts.removeAll { $0.id == account.id }
        if session?.account.id == account.id {
            session = nil
            select(accounts.first?.id)
        }
        persist()
    }

    private func api(for account: ClusterAccount) -> any ClusterAPI {
        if isDemo { return DemoCluster() }
        let token = try? keychain.token(for: account.id.uuidString)
        return APIClient(address: account.address, token: token)
    }

    private func persist() {
        guard !isDemo else { return }
        store.save(accounts: accounts, selected: session?.account.id)
    }

    // MARK: Browser sign-in

    /// Signs in to a cluster through its Studio, the way `skali remote add`
    /// does: open a device request, let the person approve it in the
    /// browser (where their Studio session and password autofill live), and
    /// take the session from the approving poll.
    ///
    /// `onCode` hands the user code to the UI so it can show what the
    /// browser will ask to approve.
    func signIn(to address: ClusterAddress, onCode: (String) -> Void) async throws {
        let client = APIClient(address: address)
        let request: DeviceRequest
        do {
            request = try await client.startDeviceLogin(clientLabel: Self.clientLabel)
        } catch let error as APIError where error.status == 404 {
            throw SignInError.unsupportedCluster
        }
        onCode(request.userCode)
        let approval = try await authorizer.approve(request, at: address) {
            try await client.pollDevice(deviceCode: $0)
        }
        guard let created = approval.session else { throw SignInError.noSession }

        let meta = try? await client.authorized(with: created.token).systemMeta()
        // Signing in to the same account again replaces it instead of adding a twin.
        let existing = accounts.first { $0.address == address && $0.user.id == created.user.id }
        let account = ClusterAccount(
            id: existing?.id ?? UUID(), address: address, instanceID: meta?.instanceId,
            instanceName: meta?.name, user: created.user)
        try keychain.setToken(created.token, for: account.id.uuidString)
        accounts.removeAll { $0.id == account.id }
        accounts.append(account)
        if session?.account.id == account.id { session = nil }
        select(account.id)
    }

    /// Re-confirms the session through the browser so a sudo-gated call
    /// (403 `reauth_required`) can be retried.
    private func reauthenticate(_ account: ClusterAccount) async throws {
        guard !isDemo else { return }
        let api = api(for: account)
        let request = try await api.startDeviceReauth(clientLabel: Self.clientLabel)
        _ = try await authorizer.approve(request, at: account.address) {
            try await api.pollDevice(deviceCode: $0)
        }
    }

    /// What the Studio shows the approver, e.g. "skali for iOS on iPad".
    private static var clientLabel: String {
        "skali for iOS on \(UIDevice.current.name)"
    }
}

enum SignInError: LocalizedError {
    case unsupportedCluster
    case noSession

    var errorDescription: String? {
        switch self {
        case .unsupportedCluster:
            "This cluster does not support browser sign-in yet. Update skalid and try again."
        case .noSession:
            "The cluster approved the sign-in but sent no session. Try again."
        }
    }
}
