import { FC } from "react";
import { useTranslation } from "react-i18next";
import { Checkbox } from "./Checkbox";

// Extracted from the old dashboard's UserFormModal.tsx, which had this
// protocol-checkboxes-then-per-protocol-tag-checkboxes block inline. Shared
// between the Users form and the new User Templates form - a purely
// controlled/presentational component on purpose: which protocols default to
// selected, whether picking a protocol also auto-ticks every one of its
// tags, and whether an untouched protocol should even be submitted at all
// are all policy decisions that differ between the two callers (Users keeps
// the old "touched protocol" tracking so an untouched one falls back to the
// backend's own "every inbound of this protocol" default; Templates has no
// such backend default to fall back to, so it always submits exactly what is
// ticked) - so none of that logic lives in here, only the rendering.
export type InboundsPickerProps = {
  /** Final, already-ordered list of protocols to render a checkbox for. */
  protocols: string[];
  /** True marks a protocol as no longer configured on this server (still
   * shown - e.g. a user's existing protocol - just flagged, per the old
   * dashboard's "(unavailable on this server)" suffix). */
  isProtocolUnavailable?: (protocol: string) => boolean;
  /** The full inbound-tag catalog per protocol, from GET /api/inbounds. */
  inboundsByProtocol: Record<string, string[]>;
  selectedProtocols: ReadonlySet<string>;
  onToggleProtocol: (protocol: string) => void;
  selectedInbounds: Record<string, ReadonlySet<string>>;
  onToggleTag: (protocol: string, tag: string) => void;
};

export const InboundsPicker: FC<InboundsPickerProps> = ({
  protocols,
  isProtocolUnavailable,
  inboundsByProtocol,
  selectedProtocols,
  onToggleProtocol,
  selectedInbounds,
  onToggleTag,
}) => {
  const { t } = useTranslation();

  if (protocols.length === 0) {
    return (
      <p className="text-sm text-rapido-muted">
        {t("rapido.noProtocolsAvailable")}
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div>
        <div className="mb-2 text-xs font-medium text-rapido-muted">
          {t("userDialog.protocols")}
        </div>
        <div className="flex flex-wrap gap-4">
          {protocols.map((protocol) => (
            <Checkbox
              key={protocol}
              checked={selectedProtocols.has(protocol)}
              onChange={() => onToggleProtocol(protocol)}
              label={
                <>
                  <span>{protocol}</span>
                  {isProtocolUnavailable?.(protocol) && (
                    <span className="ms-1 text-xs text-rapido-muted">
                      ({t("rapido.protocolUnavailable")})
                    </span>
                  )}
                </>
              }
            />
          ))}
        </div>
      </div>

      {protocols
        .filter((protocol) => selectedProtocols.has(protocol))
        .map((protocol) => {
          const tags = inboundsByProtocol[protocol] ?? [];
          if (tags.length === 0) return null;
          return (
            <div key={protocol}>
              <div className="mb-2 text-xs font-medium text-rapido-muted">
                {t("rapido.protocolInbounds", { protocol })}
              </div>
              {/* Hosts are not chosen per user/template, and an admin
                  reasonably looks for them here. One line stops that search. */}
              <p className="mb-2 text-[11px] text-rapido-muted/80">
                {t("rapido.inboundsHostsHint")}
              </p>
              <div className="flex flex-wrap gap-3 rounded-lg border border-rapido-border p-3">
                {tags.map((tag) => (
                  <Checkbox
                    key={tag}
                    checked={selectedInbounds[protocol]?.has(tag) ?? false}
                    onChange={() => onToggleTag(protocol, tag)}
                    label={tag}
                  />
                ))}
              </div>
            </div>
          );
        })}
    </div>
  );
};

export default InboundsPicker;
