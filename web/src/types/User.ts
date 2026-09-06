// Mirrors internal/httpapi/user.go's userResponseDTO/userWriteRequest -
// the Go backend's actual field set, not the old Python-targeting frontend's.
// Notably absent from the old type and present here: `online_at` (added to
// the Go DTO this same phase) and `excluded_inbounds`. Notably absent from
// here versus the old type: `links` (the Go backend only ever returns
// `subscription_url` - there is no per-format links array yet) and node
// connection states ("error"/"connecting"/"connected") that were never real
// user statuses to begin with, just leftover node-status values on the same
// old union.
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
  admin_username: string | null;
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
