import Foundation
import Testing
@testable import SkaliKit

struct ClusterAddressTests {
    @Test func bareHostGetsHTTPSAndAPIPath() throws {
        let address = try #require(ClusterAddress(userInput: " Skali.Example.com/ "))
        #expect(address.apiURL.absoluteString == "https://skali.example.com/api")
        #expect(address.studioURL.absoluteString == "https://skali.example.com")
        #expect(address.host == "skali.example.com")
    }

    @Test func explicitURLKeepsItsPath() throws {
        let address = try #require(ClusterAddress(userInput: "http://localhost:8080"))
        #expect(address.apiURL.absoluteString == "http://localhost:8080")
        let api = try #require(ClusterAddress(userInput: "https://skali.example.com/api/"))
        #expect(api.apiURL.absoluteString == "https://skali.example.com/api")
        #expect(api.studioURL.absoluteString == "https://skali.example.com")
    }

    @Test(arguments: ["", "ftp://example.com", "user:pw@example.com", "example.com?x=1", "https://"])
    func rejectsWhatIsNotAClusterAddress(_ input: String) {
        #expect(ClusterAddress(userInput: input) == nil)
    }

    @Test func verificationURLCarriesTheCode() throws {
        let address = try #require(ClusterAddress(userInput: "skali.example.com"))
        #expect(address.verificationURL(userCode: "BCDF-GHJK").absoluteString
            == "https://skali.example.com/auth/device?code=BCDF-GHJK")
    }
}
