import { FC, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Outbound, RoutingRule } from "types/CoreConfig";
import {
  isLeafOutboundType,
  otherMatcherSummary,
  outboundSupportsDirectFallback,
  setOutboundBindInterface,
  setOutboundDirectFallback,
  setRuleInboundPorts,
  setRuleInbounds,
} from "utils/coreConfigRules";
import { formatInboundPorts, parseInboundPortInput, summarizePorts } from "utils/inboundPorts";
import { Badge } from "./Badge";
import { Button } from "./Button";
import { Checkbox } from "./Checkbox";
import { Input } from "./Input";
import { ltrIsolate } from "./bidi";

type Doc = Record<string, unknown>;

export const parseDocObject = (text: string): Doc | null => {
  try {
    const parsed = JSON.parse(text);
    return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
};

const asList = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const asStrings = (v: unknown): string[] => asList(v).filter((x): x is string => typeof x === "string");

// Ports and interface names are LTR tokens: dir="ltr" keeps them intact
// inside the Persian layout, and the "(15 ports)" count stays outside that
// island so its own words read in the page's direction.
const PortList: FC<{ ports: readonly number[] }> = ({ ports }) => {
  const { t } = useTranslation();
  const { text, total, truncated } = summarizePorts(ports);
  return (
    <>
      <span dir="ltr" title={ports.join(", ")}>
        {text}
      </span>
      {truncated && <span className="ms-1">{t("rapido.xrayConfig.portsCount", { count: total })}</span>}
    </>
  );
};

const SectionTitle: FC<{ children: string }> = ({ children }) => (
  <h4 className="mb-1.5 text-xs font-medium text-rapido-muted">{children}</h4>
);

const rowClass = "rounded-lg border border-rapido-border bg-rapido-bg/40 p-2.5";

const OutboundRow: FC<{
  outbound: Outbound;
  editing: boolean;
  onToggleEdit: () => void;
  onChange: (next: Outbound) => void;
}> = ({ outbound, editing, onToggleEdit, onChange }) => {
  const { t } = useTranslation();
  const leaf = isLeafOutboundType(outbound.type);
  const eligible = outboundSupportsDirectFallback(outbound);
  const fallbackOn = outbound.direct_fallback === true;
  const members = asStrings(outbound.outbounds);
  const bindInterface = typeof outbound.bind_interface === "string" ? outbound.bind_interface : "";

  return (
    <div className={rowClass}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="truncate text-sm font-semibold" dir="ltr">
            {String(outbound.tag ?? "")}
          </span>
          <Badge tone="sky" dir="ltr">
            {String(outbound.type ?? "")}
          </Badge>
          {bindInterface && (
            <Badge tone="brand" dir="ltr">
              {bindInterface}
            </Badge>
          )}
          {fallbackOn && <Badge tone="yellow">{t("rapido.xrayConfig.directFallbackBadge")}</Badge>}
          {members.length > 0 && (
            <span className="text-xs text-rapido-muted" dir="ltr">
              {members.join(", ")}
            </span>
          )}
        </div>
        {leaf && (
          <Button variant="chip" tone={editing ? "neutral" : "accent"} onClick={onToggleEdit}>
            {editing ? t("rapido.xrayConfig.rowDone") : t("rapido.edit")}
          </Button>
        )}
      </div>

      {leaf && editing && (
        <div className="mt-2.5 flex flex-col gap-2 border-t border-rapido-border pt-2.5">
          <label className="flex flex-col gap-1 sm:max-w-xs">
            <span className="text-xs text-rapido-muted">{t("rapido.xrayConfig.outboundInterface")}</span>
            <Input
              dir="ltr"
              placeholder="wg0"
              value={bindInterface}
              onChange={(e) => onChange(setOutboundBindInterface(outbound, e.target.value))}
            />
          </label>
          {/* Shown only where the server would accept it - plus when the flag is
              already (wrongly) set, so it can still be unticked from here. */}
          {eligible || fallbackOn ? (
            <div className="flex flex-col gap-1">
              <Checkbox
                checked={fallbackOn}
                label={t("rapido.xrayConfig.directFallbackLabel")}
                onChange={(e) => onChange(setOutboundDirectFallback(outbound, e.target.checked))}
              />
              <p className="text-xs text-rapido-muted">{t("rapido.xrayConfig.directFallbackHint")}</p>
            </div>
          ) : (
            <p className="text-xs text-rapido-muted">{t("rapido.xrayConfig.directFallbackNeedsInterface")}</p>
          )}
        </div>
      )}
    </div>
  );
};

const RuleRow: FC<{
  index: number;
  rule: RoutingRule;
  knownInbounds: string[];
  editing: boolean;
  portDraft: string | undefined;
  onToggleEdit: () => void;
  onChange: (next: RoutingRule) => void;
  onPortDraft: (draft: string | null) => void;
}> = ({ index, rule, knownInbounds, editing, portDraft, onToggleEdit, onChange, onPortDraft }) => {
  const { t } = useTranslation();
  const selected = asStrings(rule.inbound);
  const ports = Array.isArray(rule.inbound_port) ? rule.inbound_port : [];
  const others = otherMatcherSummary(rule);
  // Tags this rule already names but the inbounds list doesn't (a typo, or an
  // inbound about to be removed) stay listed, so they can be unticked.
  const choices = [...knownInbounds, ...selected.filter((tag) => !knownInbounds.includes(tag))];

  const parsed = portDraft === undefined ? null : parseInboundPortInput(portDraft);
  const portError = parsed && !parsed.ok ? parsed.error : null;
  const portErrorText = portError
    ? t(`rapido.xrayConfig.portError${portError.kind[0].toUpperCase()}${portError.kind.slice(1)}`, {
        value: ltrIsolate(portError.value),
      })
    : "";

  const toggleInbound = (tag: string) => {
    const next = choices.filter((c) => (c === tag ? !selected.includes(c) : selected.includes(c)));
    if (next.length === 0) onPortDraft(null);
    onChange(setRuleInbounds(rule, next));
  };

  return (
    <div className={rowClass}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
          <span className="font-medium text-rapido-text">{t("rapido.xrayConfig.ruleLabel", { n: index + 1 })}</span>
          {selected.length > 0 ? (
            <span className="text-rapido-text" dir="ltr">
              {selected.join(", ")}
            </span>
          ) : (
            <span className="text-rapido-muted">{t("rapido.xrayConfig.ruleAnyInbound")}</span>
          )}
          {ports.length > 0 && (
            <span className="text-rapido-muted">
              {t("rapido.xrayConfig.portsLabel")}: <PortList ports={ports} />
            </span>
          )}
          {others.map((summary) => (
            <span key={summary} className="text-rapido-muted" dir="ltr">
              {summary}
            </span>
          ))}
          <span className="inline-block text-rapido-muted rtl:rotate-180" aria-hidden="true">
            &rarr;
          </span>
          <Badge tone="brand" dir="ltr">
            {String(rule.outbound_tag ?? "")}
          </Badge>
        </div>
        <Button variant="chip" tone={editing ? "neutral" : "accent"} onClick={onToggleEdit}>
          {editing ? t("rapido.xrayConfig.rowDone") : t("rapido.edit")}
        </Button>
      </div>

      {editing && (
        <div className="mt-2.5 flex flex-col gap-2.5 border-t border-rapido-border pt-2.5">
          {choices.length > 0 && (
            <div>
              <div className="mb-1.5 text-xs text-rapido-muted">{t("rapido.xrayConfig.ruleInboundLabel")}</div>
              <div className="flex flex-wrap gap-x-4 gap-y-1.5">
                {choices.map((tag) => (
                  <Checkbox
                    key={tag}
                    checked={selected.includes(tag)}
                    onChange={() => toggleInbound(tag)}
                    label={<span dir="ltr">{tag}</span>}
                  />
                ))}
              </div>
            </div>
          )}
          {selected.length > 0 && (
            <label className="flex flex-col gap-1 sm:max-w-md">
              <span className="text-xs text-rapido-muted">{t("rapido.xrayConfig.rulePortsField")}</span>
              <Input
                dir="ltr"
                inputMode="numeric"
                placeholder="20000, 20001"
                hasError={!!portError}
                aria-invalid={!!portError}
                value={portDraft ?? formatInboundPorts(ports)}
                onChange={(e) => {
                  const value = e.target.value;
                  const result = parseInboundPortInput(value);
                  onPortDraft(value);
                  if (result.ok) onChange(setRuleInboundPorts(rule, result.ports));
                }}
                onBlur={() => {
                  if (portDraft !== undefined && parseInboundPortInput(portDraft).ok) onPortDraft(null);
                }}
              />
              {portError ? (
                <span role="alert" className="text-xs text-red-400">
                  {portErrorText}
                </span>
              ) : (
                <span className="text-xs text-rapido-muted">
                  {t("rapido.xrayConfig.rulePortsHint", { example: ltrIsolate("20000, 20001") })}
                </span>
              )}
            </label>
          )}
        </div>
      )}
    </div>
  );
};

/**
 * A guided, collapsible view of the JSON document below it: inbounds with all
 * their listen ports, outbounds (bind interface + direct fallback) and routing
 * rules (inbound picker + per-port match). It owns no config state of its
 * own - every edit rewrites the JSON text through `onEdit`, so the textarea,
 * the Unapplied badge and Apply all keep working exactly as before. The only
 * local state is which rows are expanded and, via `portDrafts`, the ports text
 * a rule is mid-way through typing (an unparseable draft can't be written to
 * the JSON, so the parent needs to know about it to hold Apply back).
 */
export const XrayConfigStructure: FC<{
  text: string;
  onEdit: (nextText: string) => void;
  inboundPorts: Record<string, number[]> | undefined;
  portDrafts: Record<number, string>;
  onPortDraft: (ruleIndex: number, draft: string | null) => void;
}> = ({ text, onEdit, inboundPorts, portDrafts, onPortDraft }) => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Record<string, boolean>>({});
  const doc = useMemo(() => parseDocObject(text), [text]);

  const outbounds = asList(doc?.outbounds);
  const rules = asList(doc?.routing_rules);
  const inbounds = asList(doc?.inbounds).filter(isObject);
  const knownInbounds = inbounds.map((i) => i.tag).filter((tag): tag is string => typeof tag === "string");

  const toggleEdit = (key: string) => setEditing((prev) => ({ ...prev, [key]: !prev[key] }));

  const commit = (key: "outbounds" | "routing_rules", index: number, next: unknown) => {
    if (!doc) return;
    const list = [...asList(doc[key])];
    list[index] = next;
    onEdit(JSON.stringify({ ...doc, [key]: list }, null, 2));
  };

  return (
    <div className="mb-3 rounded-lg border border-rapido-border">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full flex-wrap items-center justify-between gap-2 px-3 py-2 text-start"
      >
        <span className="text-sm font-semibold">{t("rapido.xrayConfig.structureTitle")}</span>
        <span className="flex items-center gap-3 text-xs text-rapido-muted">
          {doc && (
            <span>
              {t("rapido.xrayConfig.structureCounts", { outbounds: outbounds.length, rules: rules.length })}
            </span>
          )}
          <span className="text-rapido-accent">
            {open ? t("rapido.xrayConfig.structureHide") : t("rapido.xrayConfig.structureShow")}
          </span>
        </span>
      </button>

      {open && (
        <div className="flex flex-col gap-4 border-t border-rapido-border p-3">
          {!doc ? (
            <p className="text-xs text-amber-300">{t("rapido.xrayConfig.structureInvalidJson")}</p>
          ) : (
            <>
              <p className="text-xs text-rapido-muted">{t("rapido.xrayConfig.structureHint")}</p>

              {inbounds.length > 0 && (
                <section>
                  <SectionTitle>{t("rapido.xrayConfig.sectionInbounds")}</SectionTitle>
                  <div className="flex flex-col gap-1.5">
                    {inbounds.map((inbound, i) => {
                      const tag = String(inbound.tag ?? "");
                      const ports = inboundPorts?.[tag] ?? [];
                      return (
                        <div key={`${tag}-${i}`} className={`${rowClass} flex flex-wrap items-center gap-x-2 gap-y-1`}>
                          <span className="truncate text-sm font-semibold" dir="ltr">
                            {tag}
                          </span>
                          <Badge tone="sky" dir="ltr">
                            {String(inbound.protocol ?? "")}
                          </Badge>
                          {ports.length > 0 && (
                            <span className="text-xs text-rapido-muted">
                              {t("rapido.xrayConfig.portsLabel")}: <PortList ports={ports} />
                            </span>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </section>
              )}

              {outbounds.some(isObject) && (
                <section>
                  <SectionTitle>{t("rapido.xrayConfig.sectionOutbounds")}</SectionTitle>
                  <div className="flex flex-col gap-1.5">
                    {outbounds.map((o, i) =>
                      isObject(o) ? (
                        <OutboundRow
                          key={i}
                          outbound={o as Outbound}
                          editing={!!editing[`o${i}`]}
                          onToggleEdit={() => toggleEdit(`o${i}`)}
                          onChange={(next) => commit("outbounds", i, next)}
                        />
                      ) : null
                    )}
                  </div>
                </section>
              )}

              {rules.some(isObject) && (
                <section>
                  <SectionTitle>{t("rapido.xrayConfig.sectionRules")}</SectionTitle>
                  <div className="flex max-h-[28rem] flex-col gap-1.5 overflow-y-auto pe-1">
                    {rules.map((r, i) =>
                      isObject(r) ? (
                        <RuleRow
                          key={i}
                          index={i}
                          rule={r as RoutingRule}
                          knownInbounds={knownInbounds}
                          editing={!!editing[`r${i}`]}
                          portDraft={portDrafts[i]}
                          onToggleEdit={() => toggleEdit(`r${i}`)}
                          onChange={(next) => commit("routing_rules", i, next)}
                          onPortDraft={(draft) => onPortDraft(i, draft)}
                        />
                      ) : null
                    )}
                  </div>
                </section>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
};
