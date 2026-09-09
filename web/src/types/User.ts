// Mirrors internal/httpapi/user.go's userResponseDTO/userWriteRequest -
// the Go backend's actual field set, not the old Python-targeting frontend's.
// Notably absent from the old type and present here: `online_at` (added to
// the Go DTO this same phase) and `excluded_inbounds`. Deliberately omits
// `links` even though the backend's single-user GET now returns it (added
// for external panel-management bot compatibility, e.g. Mirza-bot-style
// tools reading a user's share links directly - see internal/httpapi/
// system.go's marzbanCompatVersion and user.go's handleGetUser) - the
// dashboard itself never reads it, it always uses `subscription_url`, and
// node connection states ("error"/"connecting"/"connected") were never real
// user statuses to begin with, just leftover node-status values on the same
// old union. `admin` is a nested object (not a flat `admin_username` string)
// and `sub_updated_at`/`sub_last_user_agent`/`emergency_used_at` were added
// specifically to match the real Marzban wire shape a bot like Mirza-bot
// expects - the dashboard doesn't read any of these four fields today.
export type Status = "active" | "disabled" | "limited" | "expired" | "on_hold";

export type ProtocolType = "vmess" | "vless" | "trojan" | "shadowsocks";

// The Go backend accepts/returns each protocol's settings as an opaque JSON
// object (proxysettings.Settings, marshaled through json.RawMessage) - the
// frontend never needs to know the shape of any one protocol's settings, only
// which protocols are present, so this stays a loose record rather than a
// per-protocol union.
export type ProxySettingsMap = Record<string, Record<string, unknown>>;

export type DataLimitResetStrategy =
  | "no_reset"
  | "day"
  | "week"
  | "month"
  | "year";

// map[string][]string on the Go side: protocol -> inbound tags.
export type UserInbounds = Record<string, string[]>;

export type NextPlan = {
  data_limit: number;
  expire: number;
  add_remaining_traffic: boolean;
  fire_on_either: boolean;
};

// Mirrors internal/httpapi/admin.go's adminDTO - defined locally rather than
// imported from Admin.ts, matching this file's existing NextPlan precedent
// of keeping each response type self-contained.
export type UserAdmin = {
  id: number;
  username: string;
  is_sudo: boolean;
  telegram_id: number | null;
  discord_webhook: string | null;
  users_usage: number | null;
};

export type User = {
  id: number;
  username: string;
  status: Status;
  used_traffic: number;
  lifetime_used_traffic: number;
  data_limit: number | null;
  data_limit_reset_strategy: DataLimitResetStrategy;
  expire: number | null;
  note: string | null;
  created_at: string;
  on_hold_expire_duration: number | null;
  on_hold_timeout: string | null;
  auto_delete_in_days: number | null;
  sub_updated_at: string | null;
  sub_last_user_agent: string | null;
  emergency_used_at: string | null;
  admin: UserAdmin | null;
  // Non-null only for a Gateway replica (see internal/httpapi/gateway_sync.go) -
  // a real local user always has this null. The panel that pushed this user
  // out to us is the only one allowed to change it - see UsersAdmin.tsx's own
  // read-only treatment, matching the backend's own rejection of a direct edit.
  synced_from_panel_name: string | null;
  proxies: ProxySettingsMap;
  inbounds: UserInbounds;
  excluded_inbounds: UserInbounds;
  next_plan: NextPlan | null;
  subscription_url: string;
  online_at: string | null;
};

export type UsersListResponse = {
  users: User[];
  total: number;
};

// The subset userWriteRequest actually accepts, shared by create (which also
// requires `username`) and edit (which allows every field to be omitted -
// PUT /api/user/:username is a partial update, not a full replace).
export type UserWritePayload = {
  status?: Status;
  proxies?: ProxySettingsMap;
  inbounds?: UserInbounds;
  expire?: number | null;
  data_limit?: number | null;
  data_limit_reset_strategy?: DataLimitResetStrategy;
  note?: string | null;
  on_hold_expire_duration?: number | null;
  on_hold_timeout?: string | null;
  auto_delete_in_days?: number | null;
  next_plan?: NextPlan | null;
};

export type UserCreatePayload = UserWritePayload & { username: string };
