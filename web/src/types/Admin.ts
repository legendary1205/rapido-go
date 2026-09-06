// Mirrors internal/httpapi/admin.go's adminDTO. Per the plan's key fact #2,
// the field is `discord_webhook` - the old frontend's `UserApi.discord_webook`
// was a typo that must not be carried forward.
export type Admin = {
  id?: number;
  username: string;
  is_sudo: boolean;
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

// PUT /api/admin/:username: crud.update_admin's truthy-overwrite semantics
// (see admin.go's handleUpdateAdmin) - a falsy/omitted field here leaves the
// stored value untouched rather than clearing it, so every field is optional.
export type AdminModifyPayload = {
  password?: string;
  is_sudo?: boolean;
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
