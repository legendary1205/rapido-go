import { FC, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useIsSudo } from "hooks/useCurrentAdminQuery";
import { LogStream, useLogSourcesQuery, useLogStream } from "hooks/useLogsQuery";
import { LogSource } from "types/Logs";
import { LevelFilter, MAX_LOG_ENTRIES, entriesToText, filterEntries } from "utils/logLines";
import { toneForNodeStatus } from "utils/nodeStatus";
import { Badge, BadgeTone, PulseDot } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Card, CardSubtitle } from "rapido-ui/Card";
import { Input } from "rapido-ui/Input";
import { Select } from "rapido-ui/Select";
import { LogViewport } from "rapido-ui/LogViewport";

// The two sources that always exist. Shown straight away, and kept if the
// source list itself cannot be loaded, so the panel's own logs stay reachable.
const BUILTIN_SOURCES: LogSource[] = [
  { id: "panel", label: "Panel API", kind: "panel" },
  { id: "backend", label: "Backend jobs", kind: "backend" },
];

const LEVEL_OPTIONS: { value: LevelFilter; labelKey: string }[] = [
  { value: "all", labelKey: "rapido.logs.levelAll" },
  { value: "info", labelKey: "rapido.logs.levelInfo" },
  { value: "warn", labelKey: "rapido.logs.levelWarn" },
  { value: "error", labelKey: "rapido.logs.levelError" },
];

// The panel and backend are the processes answering this very request, so they
// carry no status of their own; only nodes report one.
const sourceTone = (s: LogSource): BadgeTone => (s.status ? toneForNodeStatus(s.status) : "green");

const downloadText = (text: string, filename: string) => {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }));
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
};

const stamp = () => new Date().toISOString().replace(/[:.]/g, "-").replace("T", "_").slice(0, 19);

type LiveState = "live" | "paused" | "waiting" | "connecting" | "error";

const LIVE_TONE: Record<LiveState, BadgeTone> = {
  live: "green",
  paused: "gray",
  waiting: "yellow",
  connecting: "gray",
  error: "red",
};

const LIVE_LABEL: Record<LiveState, string> = {
  live: "rapido.logs.live",
  paused: "rapido.logs.paused",
  waiting: "rapido.logs.nodeWaiting",
  connecting: "rapido.logs.connecting",
  error: "rapido.logs.disconnected",
};

const liveStateOf = (stream: LogStream, paused: boolean, isNode: boolean): LiveState => {
  if (paused) return "paused";
  if (stream.error) return "error";
  if (!stream.loaded) return "connecting";
  if (isNode && !stream.streaming) return "waiting";
  return "live";
};

const errorKey = {
  unavailable: "rapido.logs.errUnavailable",
  notFound: "rapido.logs.errNotFound",
  failed: "rapido.logs.errFailed",
} as const;

const SourceChip: FC<{ source: LogSource; active: boolean; onSelect: () => void }> = ({ source, active, onSelect }) => {
  const { t } = useTranslation();
  const label =
    source.kind === "panel" ? t("rapido.logs.sourcePanel") : source.kind === "backend" ? t("rapido.logs.sourceBackend") : source.label;
  return (
    <Button
      variant="chip"
      tone={active ? "accent" : "neutral"}
      aria-pressed={active}
      onClick={onSelect}
      className="flex items-center gap-2"
    >
      <PulseDot tone={sourceTone(source)} live={false} />
      <span className="max-w-[12rem] truncate">{label}</span>
    </Button>
  );
};

// One source's toolbar + viewport. Mounted under key={source.id}, so switching
// sources drops its buffer, cursor, pause and scroll state in one go.
const LogPane: FC<{
  source: LogSource;
  level: LevelFilter;
  onLevelChange: (level: LevelFilter) => void;
  query: string;
  onQueryChange: (query: string) => void;
}> = ({ source, level, onLevelChange, query, onQueryChange }) => {
  const { t } = useTranslation();
  const isSudo = useIsSudo();
  const [paused, setPaused] = useState(false);
  const isNode = source.kind === "node";

  const stream = useLogStream(source.id, { paused, enabled: isSudo });
  const rows = useMemo(() => filterEntries(stream.entries, level, query), [stream.entries, level, query]);
  const state = liveStateOf(stream, paused, isNode);

  const emptyText =
    stream.entries.length > 0
      ? t("rapido.logs.emptyFiltered")
      : !stream.loaded
      ? t("rapido.logs.loading")
      : isNode && !stream.streaming
      ? t("rapido.logs.nodeWaitingHint")
      : t("rapido.logs.empty");

  const download = () =>
    downloadText(entriesToText(rows), `rapido-logs-${source.id.replace(/[^a-z0-9_-]+/gi, "-")}-${stamp()}.txt`);

  return (
    <Card className="flex min-w-0 flex-col gap-3 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={LIVE_TONE[state]} role="status" data-state={state}>
          <PulseDot tone={LIVE_TONE[state]} live={state === "live"} />
          {t(LIVE_LABEL[state])}
        </Badge>

        <Select
          value={level}
          aria-label={t("rapido.logs.level")}
          onChange={(e) => onLevelChange(e.target.value as LevelFilter)}
        >
          {LEVEL_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {t(o.labelKey)}
            </option>
          ))}
        </Select>

        <Input
          type="search"
          value={query}
          placeholder={t("rapido.logs.search")}
          aria-label={t("rapido.logs.search")}
          className="min-w-[10rem] flex-1 sm:max-w-xs"
          onChange={(e) => onQueryChange(e.target.value)}
        />

        {/* ms-auto, not ml-auto: see UsersTable's "New user" button. */}
        <div className="ms-auto flex flex-wrap items-center gap-1.5">
          <Button variant="chip" tone={paused ? "accent" : "neutral"} onClick={() => setPaused((p) => !p)}>
            {paused ? t("rapido.logs.resume") : t("rapido.logs.pause")}
          </Button>
          <Button variant="chip" onClick={stream.clear} disabled={stream.entries.length === 0}>
            {t("rapido.logs.clear")}
          </Button>
          <Button variant="chip" tone="sky" onClick={download} disabled={rows.length === 0}>
            {t("rapido.logs.download")}
          </Button>
        </div>
      </div>

      {stream.error && (
        <div
          role="alert"
          className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400"
        >
          {t(errorKey[stream.error])}
        </div>
      )}

      <LogViewport rows={rows} emptyText={emptyText} />

      <CardSubtitle className="tabular-nums">
        {t("rapido.logs.count", { shown: rows.length, total: stream.entries.length })}
        {stream.entries.length >= MAX_LOG_ENTRIES && ` · ${t("rapido.logs.capped", { max: MAX_LOG_ENTRIES })}`}
      </CardSubtitle>
    </Card>
  );
};

export const Logs: FC = () => {
  const { t } = useTranslation();
  const isSudo = useIsSudo();
  const { data: fetched, isError: sourcesFailed } = useLogSourcesQuery(isSudo);
  const [sourceId, setSourceId] = useState("panel");
  const [level, setLevel] = useState<LevelFilter>("all");
  const [query, setQuery] = useState("");

  const sources = fetched && fetched.length > 0 ? fetched : BUILTIN_SOURCES;
  const source = sources.find((s) => s.id === sourceId);

  // A node that was deleted while its logs were open: fall back to the panel
  // rather than polling a source the backend now answers 404 for.
  useEffect(() => {
    if (fetched && !source) setSourceId(sources[0]?.id ?? "panel");
  }, [fetched, source, sources]);

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-xl font-semibold text-rapido-text">{t("rapido.logs.title")}</h1>
        <p className="text-sm text-rapido-muted">{t("rapido.logs.subtitle")}</p>
      </div>

      {sourcesFailed && (
        <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
          {t("rapido.logs.sourcesFailed")}
        </div>
      )}

      <div role="group" aria-label={t("rapido.logs.sources")} className="flex flex-wrap items-center gap-2">
        {sources.map((s) => (
          <SourceChip key={s.id} source={s} active={s.id === sourceId} onSelect={() => setSourceId(s.id)} />
        ))}
      </div>

      {source && (
        <LogPane
          key={source.id}
          source={source}
          level={level}
          onLevelChange={setLevel}
          query={query}
          onQueryChange={setQuery}
        />
      )}
    </div>
  );
};

export default Logs;
