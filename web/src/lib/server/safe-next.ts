// Return-to targets for the sign-in redirect. Only a same-origin path is
// honored: anything that could leave the console (a scheme, a protocol-
// relative //host, a backslash trick) collapses to the root.
export function safeNext(value: string | null | undefined): string {
	if (!value || !value.startsWith('/') || value.startsWith('//') || value.startsWith('/\\')) {
		return '/';
	}
	return value;
}
