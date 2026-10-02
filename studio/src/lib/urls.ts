// Environment selection lives in a `?env=<name>` query param (Railway-style):
// no route restructure, shareable URLs. The effective environment is resolved
// by the project layout load (which reads the param, registering the
// dependency) and exposed as `page.data.env`; components only append it to
// hrefs with this helper.

/** Append `?env=` to a resolve()-built path; passthrough when env is null. */
export function withEnv(path: string, env: string | null | undefined): string {
	return env ? `${path}?env=${encodeURIComponent(env)}` : path;
}
