// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export interface AuthUser {
	id: string;
	email: string;
	name: string;
	two_factor_enabled: boolean;
	created_at: string;
}

export interface SessionInfo {
	id: string;
	expires_at: string;
	ip_address: string;
	user_agent: string;
	created_at: string;
	current: boolean;
}

export interface TwoFactorEnrollment {
	secret: string;
	otpauth_uri: string;
	backup_codes: string[];
}
