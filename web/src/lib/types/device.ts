// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

/** A pending browser device authorization as the console sees it. */
export interface DeviceInfo {
	intent: 'login' | 'reauth';
	client_label: string;
	created_at: string;
	expires_at: string;
	/** For reauth requests: whether the bound terminal session is the caller's. */
	mine: boolean;
}
