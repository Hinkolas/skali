// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export interface DatabaseConnection {
	service: string;
	phase: string;
	engine: string;
	major: number;
	isolation: string;
	availability: string;
	host?: string;
	port?: number;
	database?: string;
	credential_version?: number;
}

/** Sudo-gated reveal; values are shown once and never stored client-side. */
export interface DatabaseCredentials {
	username: string;
	password: string;
	url: string;
}

export interface BucketConnection {
	service: string;
	phase: string;
	visibility: string;
	storage_quota_bytes?: number;
	endpoint?: string;
	bucket?: string;
	region?: string;
	credential_version?: number;
}

export interface BucketCredentials {
	access_key: string;
	secret_key: string;
}
