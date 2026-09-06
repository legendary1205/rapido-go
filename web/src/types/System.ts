// Mirrors internal/httpapi/system.go's systemStatsDTO/usagePointDTO - the two
// brand-new endpoints this phase adds. incoming_bandwidth/outgoing_bandwidth
// are always 0 today (no usage-reporting pipeline from nodes yet), and
// usage-history points are real calendar dates with usage:0, both honestly so
// per that file's own comments rather than silently faked here.
export type SystemStats = {
  total_user: number;
  online_users: number;
  users_active: number;
  users_on_hold: number;
  users_disabled: number;
  users_expired: number;
  users_limited: number;
  incoming_bandwidth: number;
  outgoing_bandwidth: number;
};

export type UsagePoint = {
  date: string;
  usage: number;
};
