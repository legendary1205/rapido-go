// Every admin screen redefined this pair locally with identical values -
// AdminsAdmin, InactiveAdmins, HostsAdmin, NodesAdmin, TicketsAdmin,
// CoreSettings, Integrations. One shared copy means a future palette change
// (like this one) only has one place to get right instead of seven.
export const btnBase =
  "rounded-md border px-2.5 py-1 text-xs font-medium transition-colors disabled:opacity-40 disabled:cursor-not-allowed";

export const tones = {
  neutral:
    "border-rapido-border text-rapido-muted hover:border-rapido-accent hover:bg-rapido-accent/10 hover:text-rapido-text",
  accent:
    "border-rapido-accent/40 text-rapido-accent hover:border-rapido-accent hover:bg-rapido-accent/15",
  sky: "border-sky-500/40 text-sky-400 hover:border-sky-500 hover:bg-sky-500/15",
  amber:
    "border-amber-500/40 text-amber-400 hover:border-amber-500 hover:bg-amber-500/15",
  red: "border-red-500/40 text-red-400 hover:border-red-500 hover:bg-red-500/15",
} as const;

export type ButtonTone = keyof typeof tones;
