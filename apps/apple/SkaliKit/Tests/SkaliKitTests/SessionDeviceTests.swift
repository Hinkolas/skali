import Testing
@testable import SkaliKit

struct SessionDeviceTests {
    @Test func recognizesTheCLI() {
        let device = SessionDevice(userAgent: "skali/v0.1.3-rc.3 (KiloHertz-Mac-Mini)")
        #expect(device.kind == .cli)
        #expect(device.title == "Skali CLI v0.1.3-rc.3")
        #expect(device.host == "KiloHertz-Mac-Mini")
    }

    @Test func recognizesThisApp() {
        let ua = SessionDevice.appUserAgent(version: "0.1.0", model: "iPad", system: "iPadOS", systemVersion: "27.1")
        let device = SessionDevice(userAgent: ua)
        #expect(device.kind == .app)
        #expect(device.title == "Skali for iPadOS")
        #expect(device.host == "iPad")
        #expect(device.isTablet)
    }

    @Test(arguments: [
        ("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0 Safari/605.1.15", "Safari on macOS", false),
        ("Mozilla/5.0 (iPhone; CPU iPhone OS 27_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0 Mobile/15E148 Safari/604.1", "Safari on iOS", true),
        ("Mozilla/5.0 (Macintosh; Intel Mac OS X 15.0; rv:140.0) Gecko/20100101 Firefox/140.0", "Firefox on macOS", false),
        ("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Edg/140.0", "Edge on Windows", false),
    ])
    func recognizesBrowsers(ua: String, title: String, mobile: Bool) {
        let device = SessionDevice(userAgent: ua)
        #expect(device.kind == .browser)
        #expect(device.title == title)
        #expect(device.isMobile == mobile)
    }

    @Test func fallsBackToTheRawString() {
        #expect(SessionDevice(userAgent: "curl/8.7.1").title == "curl/8.7.1")
        #expect(SessionDevice(userAgent: nil).title == "Unknown device")
    }
}
