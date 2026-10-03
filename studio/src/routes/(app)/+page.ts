import { redirect } from '@sveltejs/kit';

// The org "Projects" page is the app's home until the cluster dashboard
// ships; the logo in the Sidebar links to the same place.
export function load(): never {
	redirect(302, '/projects');
}
