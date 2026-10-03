/**
 * Lets an element rise into place the first time it scrolls into view.
 *
 * Only elements still below the fold when the page hydrates are held back,
 * so nothing on screen blinks out and back in, and a page whose scripts
 * never run shows everything where it belongs. The styles live in
 * routes/layout.css under [data-reveal].
 */
export function reveal(node: HTMLElement, { delay = 0 }: { delay?: number } = {}) {
	if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
	if (node.getBoundingClientRect().top < innerHeight) return;

	node.dataset.reveal = 'pending';
	node.style.setProperty('--reveal-delay', `${delay}ms`);

	const observer = new IntersectionObserver(
		([entry]) => {
			if (!entry.isIntersecting) return;
			node.dataset.reveal = 'shown';
			observer.disconnect();
		},
		{ rootMargin: '0px 0px -12% 0px' }
	);
	observer.observe(node);

	return { destroy: () => observer.disconnect() };
}
