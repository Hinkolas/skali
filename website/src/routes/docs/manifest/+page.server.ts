import { contentRef, currentRelease } from '$lib/server/release';
import { manifestReference, manifestReferencePath } from '$lib/server/reference';

// Static text with nothing to hydrate: shipping no client code also keeps
// the rendered reference from being sent a second time as page data.
export const csr = false;

export const load = () => {
	const ref = contentRef();
	return { release: currentRelease(), ref, path: manifestReferencePath, ...manifestReference(ref) };
};
