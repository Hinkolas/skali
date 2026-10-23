// Best-effort user-agent parsing for the session list. Deliberately tiny:
// it recognizes skali's own clients, then turns the major browser/OS
// combinations into a friendly label, and returns null so callers can fall
// back to the raw string. The Apple app labels sessions the same way
// (apps/apple/SkaliKit/Sources/SkaliKit/Presentation/SessionDevice.swift).

export interface DeviceInfo {
	kind: 'cli' | 'app' | 'browser' | 'system';
	label: string;
	/** The machine the client named: the CLI's hostname, the app's device model. */
	host: string | null;
	mobile: boolean;
	tablet: boolean;
}

// skali/<version> (<host>), the CLI (cmd/skali/main.go).
const CLI = /^skali\/(\S+)(?: \((.+)\))?$/;
// skali-ios/<version> (<model>; <system> <version>), the Apple app.
const APP = /^skali-ios\/(\S+)(?: \(([^;)]+)(?:; ([^)]+))?\))?$/;

// Order matters: Chrome UAs contain "Safari", Edge/Opera UAs contain
// "Chrome", so the more specific tokens must win first. The iOS variants
// (CriOS, FxiOS, EdgiOS) never contain their desktop token.
const BROWSERS: [RegExp, string][] = [
	[/\bEdg(?:e|A|iOS)?\//, 'Edge'],
	[/\bOPR\/|\bOpera\b/, 'Opera'],
	[/\bSamsungBrowser\//, 'Samsung Internet'],
	[/\bFirefox\/|\bFxiOS\//, 'Firefox'],
	[/\bChrome\/|\bCriOS\/|\bChromium\//, 'Chrome'],
	[/\bSafari\//, 'Safari']
];

// Android before Linux (Android UAs contain "Linux"), iPhone/iPad before
// macOS (their UAs contain "like Mac OS X").
const SYSTEMS: [RegExp, string][] = [
	[/\bWindows\b/, 'Windows'],
	[/\biPhone\b|\biPod\b/, 'iOS'],
	[/\biPad\b/, 'iPadOS'],
	[/\bMac OS X\b|\bMacintosh\b/, 'macOS'],
	[/\bAndroid\b/, 'Android'],
	[/\bCrOS\b/, 'ChromeOS'],
	[/\bLinux\b|\bX11\b/, 'Linux']
];

export function describeUserAgent(ua: string): DeviceInfo | null {
	if (!ua) return null;
	ua = ua.trim();

	const cli = CLI.exec(ua);
	if (cli) {
		return {
			kind: 'cli',
			label: `skali CLI ${cli[1]}`,
			host: cli[2] ?? null,
			mobile: false,
			tablet: false
		};
	}
	const app = APP.exec(ua);
	if (app) {
		const model = app[2] ?? null;
		const tablet = model?.startsWith('iPad') ?? false;
		return {
			kind: 'app',
			label: tablet ? 'skali for iPadOS' : 'skali for iOS',
			host: model,
			mobile: true,
			tablet
		};
	}

	const browser = BROWSERS.find(([re]) => re.test(ua))?.[1] ?? null;
	const os = SYSTEMS.find(([re]) => re.test(ua))?.[1] ?? null;
	if (!browser && !os) return null;
	return {
		kind: browser ? 'browser' : 'system',
		label: browser && os ? `${browser} on ${os}` : (browser ?? os!),
		host: null,
		mobile: /\bMobi|\biPhone\b|\biPad\b|\bAndroid\b/.test(ua),
		tablet: /\biPad\b/.test(ua)
	};
}
