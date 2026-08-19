// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

/** Instance-wide role: admins may do everything; members hold nothing until granted project membership. */
export type Role = 'admin' | 'member';

export interface AuthUser {
	id: string;
	email: string;
	name: string;
	role: Role;
	/** Whether a member may create projects; instance admins always may. */
	create_projects: boolean;
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
