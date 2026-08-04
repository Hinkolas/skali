// Unauthenticated liveness probe for the in-cluster deployment; the kubelet
// hits the pod directly on its port, so this never needs the edge or a
// session. hooks.server.ts only calls the API when a session cookie is
// present, so probes cost no upstream round trip.
export const GET = () => new Response('ok');
