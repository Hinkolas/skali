import { redirect } from '@sveltejs/kit';

// The org "Projects" page is the app's home for now.
export function load(): never {
	redirect(302, '/projects');
}
