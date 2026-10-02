// Best-effort user-agent parsing for the session list. Deliberately tiny:
// it only needs to turn the major browser/OS combinations into a friendly
// label, and returns null so callers can fall back to the raw string.

export interface DeviceInfo {
	label: string;
	mobile: boolean;
}

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
	const browser = BROWSERS.find(([re]) => re.test(ua))?.[1] ?? null;
	const os = SYSTEMS.find(([re]) => re.test(ua))?.[1] ?? null;
	if (!browser && !os) return null;
	return {
		label: browser && os ? `${browser} on ${os}` : (browser ?? os!),
		mobile: /\bMobi|\biPhone\b|\biPad\b|\bAndroid\b/.test(ua)
	};
}
