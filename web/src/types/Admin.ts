// Mirrors internal/httpapi/admin.go's adminDTO. Per the plan's key fact #2,
// the field is `discord_webhook` - the old frontend's `UserApi.discord_webook`
// was a typo that must not be carried forward.
export type Admin = {
  id?: number;
  username: string;
  is_sudo: boolean;
  // One tier above sudo - can grant/revoke sudo (even on other sudo
  // admins) and appoint/remove other owners. See handleUpdateAdmin's own
  // comment for why granting/revoking either flag needed a real
  // permission check instead of the old "sudo can only ever be granted,
  // never revoked" workaround.
  is_owner: boolean;
  telegram_id: number | null;
  discord_webhook: string | null;
  users_usage: number | null;
};

export type AdminCreatePayload = {
  username: string;
  password: string;
  is_sudo: boolean;
  telegram_id?: number | null;
  discord_webhook?: string | null;
};

// PUT /api/admin/:username: is_sudo/is_owner are real tri-state fields here
// (omit = leave unchanged, true/false = set) - unlike every other field,
// which keeps the old truthy-overwrite semantics (a falsy/omitted value
// leaves the stored value untouched rather than clearing it).
export type AdminModifyPayload = {
  password?: string;
  is_sudo?: boolean;
  is_owner?: boolean;
  telegram_id?: number | null;
  discord_webhook?: string | null;
};

export type InactiveAdminRow = {
  username: string;
  last_activity: string;
  user_count: number;
};

export type InactiveAdminsResult = {
  cutoff_days: number;
  admins: InactiveAdminRow[];
};

export type InactiveAdminsDeleted = InactiveAdminsResult & {
  users_removed: number;
};
