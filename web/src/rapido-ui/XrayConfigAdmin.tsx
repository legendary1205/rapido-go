import { FC, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useXrayConfigQuery, useSaveXrayConfigMutation } from "hooks/useXrayConfigQuery";
import { useImportXrayConfigMutation } from "hooks/useInboundsQuery";
import { XrayConfig, XrayConfigWritePayload } from "types/XrayConfig";
import { XrayImportResult } from "types/XrayImport";
import { errorText } from "service/errors";
import { locateJSONSyntaxError } from "utils/jsonSyntaxLocator";
import { Card } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Modal } from "rapido-ui/Modal";

// parseXrayConfigJSON is deliberately lenient about the core-config fields
// (same reasoning as coreConfigHelpers.ts's own parseFullConfigJSON: only
// guarantee the list fields are real arrays so nothing downstream crashes
// on a hand-edited blob, everything else passes through to the server's
// own validateCoreConfig) but strict about `inbounds` being an array - the
// server always sends it, and silently defaulting a missing/malformed one
// to [] would mean "delete every inbound" is one typo away from "no
// inbounds section" on Apply. It isn't actually destructive server-side
// (syncInboundEntries only ever upserts, see xrayconfig.go's own doc
// comment), but failing loudly here is still the more honest response to
// a broken paste than silently proceeding with zero inbounds.
const parseXrayConfigJSON = (text: string): XrayConfigWritePayload => {
  const parsed = JSON.parse(text);
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new Error("rapido.xrayConfig.jsonMustBeObject");
  }
  if (!Array.isArray(parsed.inbounds)) {
    throw new Error("rapido.xrayConfig.jsonInboundsMustBeArray");
  }
  return {
    log_level: typeof parsed.log_level === "string" ? parsed.log_level : "warn",
    sniff_enabled: !!parsed.sniff_enabled,
    outbounds: Array.isArray(parsed.outbounds) ? parsed.outbounds : [],
    routing_rules: Array.isArray(parsed.routing_rules) ? parsed.routing_rules : [],
    dns_servers: Array.isArray(parsed.dns_servers) ? parsed.dns_servers : [],
    inbounds: parsed.inbounds,
  };
};

// Turns a caught error from parseXrayConfigJSON into what the editor shows:
// a genuine JSON.parse SyntaxError gets run back through locateJSONSyntaxError
// (see that module's own doc comment on why - native error messages aren't a
// reliable source of a line number) and, when that succeeds, a line-numbered
// message plus the line itself (so the gutter can highlight it too); the two
// structural checks parseXrayConfigJSON throws itself (not-an-object,
// inbounds-not-an-array) describe the whole document rather than one line, so
// they intentionally surface with no line number.
const describeJSONError = (
  text: string,
  error: unknown,
  t: (key: string, opts?: Record<string, unknown>) => string
): { message: string; line: number | null } => {
  if (error instanceof SyntaxError) {
    const loc = locateJSONSyntaxError(text);
    if (loc) {
      return { message: t("rapido.xrayConfig.jsonLineError", { line: loc.line, message: loc.message }), line: loc.line };
    }
    return { message: error.message, line: null };
  }
  return { message: error instanceof Error ? t(error.message) : String(error), line: null };
};

const XrayConfigJSONEditor: FC<{ config: XrayConfig }> = ({ config }) => {
  const { t } = useTranslation();
  const [text, setText] = useState(() => JSON.stringify(config, null, 2));
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [errorLine, setErrorLine] = useState<number | null>(null);
  const [previousText, setPreviousText] = useState<string | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const gutterRef = useRef<HTMLDivElement>(null);
  const lineCount = text.split("\n").length;
  // Set only while a parsed pending payload is genuinely about to delete one
  // or more inbounds (omitted from the JSON - see xrayconfig.go's own doc
  // comment on why that's real deletion here, not a no-op like the old
  // per-tag upsert-only sync was) - holds the tags themselves so the
  // confirmation can name them, and the already-parsed payload so
  // confirming doesn't re-parse text that may have changed underneath it.
  const [pendingRemoval, setPendingRemoval] = useState<{ tags: string[]; payload: XrayConfigWritePayload } | null>(
    null
  );
  const save = useSaveXrayConfigMutation();

  // Re-seed from the server's own copy whenever it changes AND there's no
  // unapplied edit sitting in the textarea - a background refetch (e.g. the
  // embedded HostsAdmin creating a new inbound's default host elsewhere)
  // must never clobber something the admin is mid-way through typing here.
  useEffect(() => {
    if (!dirty) setText(JSON.stringify(config, null, 2));
  }, [config, dirty]);

  const doApply = (payload: XrayConfigWritePayload) => {
    const before = JSON.stringify(config, null, 2);
    save.mutate(payload, {
      onSuccess: () => {
        setPreviousText(before);
        setDirty(false);
        setPendingRemoval(null);
      },
      onError: (e) => setError(errorText(e, t("rapido.xrayConfig.applyFailed"))),
    });
  };

  const apply = () => {
    setError("");
    setErrorLine(null);
    setPendingRemoval(null);
    try {
      const payload = parseXrayConfigJSON(text);
      const currentTags = new Set(config.inbounds.map((i) => i.tag));
      const nextTags = new Set(payload.inbounds.map((i) => i.tag));
      const removed = [...currentTags].filter((tag) => !nextTags.has(tag));
      if (removed.length > 0) {
        setPendingRemoval({ tags: removed, payload });
        return;
      }
      doApply(payload);
    } catch (e) {
      const described = describeJSONError(text, e, t);
      setError(described.message);
      setErrorLine(described.line);
    }
  };

  const discard = () => {
    setDirty(false);
    setError("");
    setErrorLine(null);
    setPendingRemoval(null);
    setText(JSON.stringify(config, null, 2));
  };

  const revert = () => {
    if (!previousText) return;
    setError("");
    setErrorLine(null);
    try {
      const payload = parseXrayConfigJSON(previousText);
      save.mutate(payload, {
        onSuccess: () => {
          setPreviousText(null);
          setDirty(false);
        },
        onError: (e) => setError(errorText(e, t("rapido.xrayConfig.revertFailed"))),
      });
    } catch (e) {
      const described = describeJSONError(previousText, e, t);
      setError(described.message);
      setErrorLine(described.line);
    }
  };

  const syncGutterScroll = () => {
    if (textareaRef.current && gutterRef.current) {
      gutterRef.current.scrollTop = textareaRef.current.scrollTop;
    }
  };

  return (
    <Card className="p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold">{t("rapido.xrayConfig.jsonTitle")}</h3>
          <p className="text-xs text-rapido-muted">{t("rapido.xrayConfig.jsonDesc")}</p>
        </div>
        {dirty && <Badge tone="yellow">{t("rapido.xrayConfig.unapplied")}</Badge>}
      </div>

      <div className="relative">
        {/* Absolutely positioned against this wrapper, whose own height is
            just "however tall the in-flow textarea below currently is" -
            so a manual resize-y drag on the textarea grows this wrapper too,
            and the gutter (inset-y-0) stretches to match with no JS needed.
            Only the vertical scroll offset needs syncing by hand (below),
            since the gutter's own content can be taller than its visible
            box once there are more lines than fit. */}
        <div
          ref={gutterRef}
          aria-hidden="true"
          className="pointer-events-none absolute inset-y-0 left-0 w-9 overflow-hidden rounded-l-lg border-r border-rapido-border bg-rapido-surface-2 py-2 text-right font-mono text-xs leading-5 text-rapido-muted"
        >
          {Array.from({ length: lineCount }, (_, i) => i + 1).map((n) => (
            <div key={n} className={n === errorLine ? "pr-2 font-semibold text-red-400" : "pr-2"}>
              {n}
            </div>
          ))}
        </div>
        <textarea
          ref={textareaRef}
          dir="ltr"
          spellCheck={false}
          wrap="off"
          rows={22}
          onScroll={syncGutterScroll}
          className="w-full resize-y whitespace-pre overflow-x-auto rounded-lg border border-rapido-border bg-rapido-bg py-2 pl-11 pr-3 font-mono text-xs leading-5 text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            setDirty(true);
            setError("");
            setErrorLine(null);
            setPendingRemoval(null);
          }}
        />
      </div>
      {error && (
        <div className="mt-2 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {error}
        </div>
      )}
      {pendingRemoval && (
        <div className="mt-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
          {t("rapido.xrayConfig.removalWarning", { tags: pendingRemoval.tags.join(", ") })}
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-rapido-muted">
          {dirty ? t("rapido.xrayConfig.unappliedHint") : t("rapido.xrayConfig.inSync")}
        </span>
        <div className="flex gap-2">
          <Button variant="chip" disabled={!dirty} onClick={discard}>
            {t("rapido.xrayConfig.discard")}
          </Button>
          {pendingRemoval ? (
            <Button
              variant="chip"
              tone="amber"
              disabled={save.isPending}
              onClick={() => doApply(pendingRemoval.payload)}
            >
              {save.isPending ? t("rapido.pleaseWait") : t("rapido.xrayConfig.applyConfirmRemoval")}
            </Button>
          ) : (
            <Button variant="chip" tone="accent" disabled={!dirty || save.isPending} onClick={apply}>
              {save.isPending ? t("rapido.pleaseWait") : t("rapido.xrayConfig.apply")}
            </Button>
          )}
        </div>
      </div>

      {!dirty && previousText && (
        <div className="mt-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rapido-border p-2">
          <div>
            <p className="text-xs text-rapido-text">{t("rapido.xrayConfig.previousVersionAvailable")}</p>
            <p className="text-xs text-rapido-muted">{t("rapido.xrayConfig.revertNote")}</p>
          </div>
          <Button variant="chip" disabled={save.isPending} onClick={revert}>
            {t("rapido.xrayConfig.revert")}
          </Button>
        </div>
      )}
    </Card>
  );
};

// ---------------------------------------------------------------------------

// XrayImportModal is a two-step preview-then-apply flow around POST
// /api/inbounds/import-xray: "Preview" (confirm:false) always runs first
// and shows the parsed counts/warnings without writing anything, and only
// once that's happened does "Apply" (confirm:true) - re-running the exact
// same parse server-side, not reusing the preview's client-side result -
// become available. Re-editing the pasted text after a preview resets
// back to preview-only, so Apply can never fire against text the admin
// hasn't actually previewed. Moved here unchanged from the old
// InboundsAdmin.tsx when that page merged into this one - still the same
// real-world "paste your existing Xray config, migrate it" entry point.
const XrayImportModal: FC<{ onClose: () => void; onApplied: () => void }> = ({ onClose, onApplied }) => {
  const { t } = useTranslation();
  const [config, setConfig] = useState("");
  const [preview, setPreview] = useState<XrayImportResult | null>(null);
  const [applied, setApplied] = useState<XrayImportResult | null>(null);
  const [error, setError] = useState("");
  const importXray = useImportXrayConfigMutation();

  const runPreview = () => {
    setError("");
    setApplied(null);
    importXray.mutate(
      { config, confirm: false },
      {
        onSuccess: (result) => setPreview(result),
        onError: (e) => setError(errorText(e, t("rapido.inbounds.xrayImportFailed"))),
      }
    );
  };

  const runApply = () => {
    setError("");
    importXray.mutate(
      { config, confirm: true },
      {
        onSuccess: (result) => {
          setApplied(result);
          onApplied();
        },
        onError: (e) => setError(errorText(e, t("rapido.inbounds.xrayImportFailed"))),
      }
    );
  };

  const summary = applied ?? preview;

  return (
    <Modal onClose={onClose} className="max-w-2xl">
      <h2 className="mb-1 text-lg font-semibold">{t("rapido.inbounds.xrayImportTitle")}</h2>
      <p className="mb-4 text-xs text-rapido-muted">{t("rapido.inbounds.xrayImportSubtitle")}</p>
      <div className="flex flex-col gap-3">
        <textarea
          dir="ltr"
          rows={12}
          value={config}
          onChange={(e) => {
            setConfig(e.target.value);
            setPreview(null);
            setApplied(null);
          }}
          placeholder='{ "inbounds": [...], "outbounds": [...], "routing": {...} }'
          className="w-full resize-y rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
        />

        {summary && (
          <div className="rounded-lg border border-rapido-border p-3 text-xs">
            <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1 font-medium">
              <span>{t("rapido.inbounds.xrayImportInbounds", { count: summary.inbounds_created })}</span>
              <span>{t("rapido.inbounds.xrayImportOutbounds", { count: summary.outbounds_saved })}</span>
              <span>{t("rapido.inbounds.xrayImportRules", { count: summary.routing_rules_saved })}</span>
              <span>{t("rapido.inbounds.xrayImportDns", { count: summary.dns_servers_saved })}</span>
            </div>
            {summary.warnings.length > 0 && (
              <ul className="list-inside list-disc space-y-0.5 text-amber-400">
                {summary.warnings.map((w, i) => (
                  <li key={i}>{w}</li>
                ))}
              </ul>
            )}
            {applied && <p className="mt-2 text-emerald-400">{t("rapido.inbounds.xrayImportApplied")}</p>}
          </div>
        )}

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {applied ? t("rapido.close") : t("cancel")}
          </Button>
          {!applied && (
            <>
              <Button variant="chip" tone="accent" disabled={!config.trim() || importXray.isPending} onClick={runPreview}>
                {importXray.isPending && !preview ? t("rapido.pleaseWait") : t("rapido.inbounds.xrayImportPreview")}
              </Button>
              {preview && (
                <Button variant="chip" tone="accent" disabled={importXray.isPending} onClick={runApply}>
                  {importXray.isPending ? t("rapido.pleaseWait") : t("rapido.inbounds.xrayImportApply")}
                </Button>
              )}
            </>
          )}
        </div>
      </div>
    </Modal>
  );
};

// ---------------------------------------------------------------------------

// The Xray/sing-box config as one JSON document (log level, sniffing,
// outbounds, routing rules, DNS servers, AND inbounds - all one document,
// one Apply, replacing what used to be the separate Core Config and
// Inbounds pages). Hosts deliberately stays its own standalone page
// (rapido-ui/HostsAdmin.tsx via the "hosts" nav item) rather than being
// embedded here too - an earlier version of this page embedded HostsAdmin
// directly below the JSON editor, but the user asked for that removed:
// one place for hosts, not two.
export const XrayConfigAdmin: FC = () => {
  const { t } = useTranslation();
  const { data, isLoading, isError } = useXrayConfigQuery();
  const [importing, setImporting] = useState(false);

  if (isError) {
    return (
      <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
        {t("rapido.xrayConfig.loadFailed")}
      </div>
    );
  }
  if (isLoading || !data) {
    return <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>;
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="flex justify-end">
        <Button variant="chip" onClick={() => setImporting(true)}>
          {t("rapido.inbounds.xrayImportTitle")}
        </Button>
      </div>

      <XrayConfigJSONEditor config={data} />

      {importing && <XrayImportModal onClose={() => setImporting(false)} onApplied={() => {}} />}
    </div>
  );
};

export default XrayConfigAdmin;
