// The user directory as a lookup for the actor ids runs record. One fetch
// per page life (/v1/users is open to every authenticated user, see
// AddMemberModal); a failed fetch leaves the map empty so callers fall back
// to short ids, and the next load() tries again.

import { api } from '$lib/api/client';
import type { DirectoryUser } from '$lib/types/auth';

class UsersStore {
	private byId = $state<ReadonlyMap<string, DirectoryUser>>(new Map());
	loaded = $state(false);
	private pending: Promise<void> | null = null;

	/** Idempotent: repeat calls share the in-flight or finished fetch. */
	load(): Promise<void> {
		if (this.pending) return this.pending;
		this.pending = api
			.get<{ users: DirectoryUser[] }>('/v1/users')
			.then((res) => {
				// Reassigned, not mutated: a Map inside $state is not deeply
				// proxied, the new reference is what re-renders templates.
				this.byId = new Map(res.users.map((u) => [u.id, u]));
			})
			.catch(() => {
				this.pending = null;
			})
			.finally(() => {
				this.loaded = true;
			});
		return this.pending;
	}

	/** Arrow field so it can be passed to describeActor unbound; reads $state, so templates re-render when the directory arrives. */
	resolve = (id: string): DirectoryUser | undefined => this.byId.get(id);
}

export const users = new UsersStore();
