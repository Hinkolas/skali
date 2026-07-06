import { error } from '@sveltejs/kit';

// Unmatched in-app URLs (any depth) 404 inside the app shell via the (app)
// error boundary instead of falling through to the bare root one. Explicit
// routes and [section] always win over this rest param.
export function load(): never {
	error(404, 'Not found');
}
