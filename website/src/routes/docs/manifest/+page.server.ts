import { contentRef, currentRelease } from '#lib/server/release.ts';
import { manifestReference, manifestReferencePath } from '#lib/server/reference.ts';

// Static text with nothing to hydrate: shipping no client code also keeps
// the rendered reference from being sent a second time as page data.
export const csr = false;

export const load = () => {
	const ref = contentRef();
	return { release: currentRelease(), ref, path: manifestReferencePath, ...manifestReference(ref) };
};
