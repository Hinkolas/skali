// Package platform names the skalid-owned platform namespace and the
// in-cluster coordinates of the shared services environments reach across
// namespaces. It imports nothing, so the renderer, the substrate, and the
// bundle can all name the same Service without a dependency cycle.
package platform

// Namespace is the skalid-owned platform namespace holding every substrate
// pool, tenant object, and credential Secret.
const Namespace = "skali-platform"

// S3Service and S3Port address the shared S3 gateway (the SeaweedFS filer's
// embedded S3 API) inside Namespace. Bucket routes point the edge at it
// from environment namespaces.
const (
	S3Service = "seaweed-s3"
	S3Port    = 8333
)

// InternalS3Endpoint is the in-cluster S3 gateway URL every bucket
// publishes as internal_endpoint, and the endpoint of a bucket without a
// route. With one store per installation it is a constant.
func InternalS3Endpoint() string {
	return "http://" + S3Service + "." + Namespace + ".svc.cluster.local:8333"
}
