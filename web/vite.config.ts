import tailwindcss from '@tailwindcss/vite';
import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			adapter: adapter({ fallback: '200.html', strict: true }),
			// The console ships inside skalid, so a platform update swaps the
			// bundle under open tabs. Polling _app/version.json lets the shell
			// notice and offer a reload instead of running stale code until a
			// missing chunk forces one. vite dev serves a constant version.
			version: { pollInterval: 60_000 }
		})
	],
	server: { proxy: { '/api': { target: 'http://localhost:7070', changeOrigin: false } } }
});
