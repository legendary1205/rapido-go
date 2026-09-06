import { FC, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import { fetch } from "service/http";
import { errorText } from "service/errors";
import { IntegrationSettingsStatus } from "types/Integrations";
import {
  buildIntegrationsPatch,
  IntegrationFieldSpec,
} from "utils/buildIntegrationsPatch";
import { Card } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";

// Ported near-verbatim from the old dashboard's rapido-ui/Integrations.tsx,
// minus BackupsCard (database backups are a separate later phase - see the
// plan's context) and with its inline body-building loop promoted to the
// tested utils/buildIntegrationsPatch.ts. This intentionally keeps its own
// local useState rather than a TanStack Query hook: the masked "current
// value" display plus per-field draft/cleared tri-state doesn't fit a plain
// query cache any better here than it did in the old dashboard.

type FieldConfig = IntegrationFieldSpec & { labelKey: string };

const TELEGRAM_FIELDS: FieldConfig[] = [
  { key: "telegram_api_token", labelKey: "rapido.integrations.telegramApiToken", kind: "password" },
  { key: "telegram_admin_ids", labelKey: "rapido.integrations.telegramAdminIds", kind: "intArray" },
  { key: "telegram_proxy_url", labelKey: "rapido.integrations.telegramProxyUrl", kind: "password" },
  {
    key: "telegram_logger_channel_id",
    labelKey: "rapido.integrations.telegramLoggerChannelId",
    kind: "int",
  },
  {
    key: "telegram_logger_topic_id",
    labelKey: "rapido.integrations.telegramLoggerTopicId",
    kind: "int",
  },
  {
    key: "telegram_default_vless_flow",
    labelKey: "rapido.integrations.telegramDefaultVlessFlow",
    kind: "text",
  },
];

const DISCORD_FIELDS: FieldConfig[] = [
  { key: "discord_webhook_url", labelKey: "rapido.integrations.discordWebhookUrl", kind: "password" },
];

const KIRBOT_FIELDS: FieldConfig[] = [
  { key: "kirbot_secret", labelKey: "rapido.integrations.kirbotSecret", kind: "password" },
  { key: "kirbot_url", labelKey: "rapido.integrations.kirbotUrl", kind: "text" },
  { key: "kirbot_license", labelKey: "rapido.integrations.kirbotLicense", kind: "password" },
];

const WEBHOOK_FIELDS: FieldConfig[] = [
  { key: "webhook_addresses", labelKey: "rapido.integrations.webhookAddresses", kind: "strArray" },
  { key: "webhook_secret", labelKey: "rapido.integrations.webhookSecret", kind: "password" },
];

const formatTime = (locale: string, iso?: string | null): string => {
  if (!iso) return "";
  const d = new Date(/(?:Z|[+-]\d{2}:?\d{2})$/.test(iso) ? iso : `${iso}Z`);
  if (Number.isNaN(d.getTime())) return iso;
  try {
    return d.toLocaleString(locale, { dateStyle: "medium", timeStyle: "short" });
  } catch {
    return d.toLocaleString();
  }
};

const currentDisplay = (
  status: IntegrationSettingsStatus,
  f: FieldConfig,
  t: (k: string) => string
): string => {
  const v = status[f.key as keyof IntegrationSettingsStatus];
  if (f.kind === "intArray" || f.kind === "strArray") {
    const arr = (v as (number | string)[] | undefined) ?? [];
    return arr.length ? arr.join(", ") : t("rapido.integrations.notSet");
  }
  if (v === null || v === undefined || v === "") return t("rapido.integrations.notSet");
  return String(v);
};

// ---------------------------------------------------------------------------

const FieldRow: FC<{
  field: FieldConfig;
  status: IntegrationSettingsStatus;
  draft: string;
  cleared: boolean;
  onDraftChange: (v: string) => void;
  onToggleClear: () => void;
}> = ({ field, status, draft, cleared, onDraftChange, onToggleClear }) => {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-1 border-t border-rapido-border py-3 first:border-t-0 first:pt-0">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium">{t(field.labelKey)}</span>
        <span className="text-xs text-rapido-muted" dir="ltr">
          {t("rapido.integrations.current")}: {currentDisplay(status, field, t)}
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          className="flex-1"
          dir="ltr"
          type={field.kind === "password" ? "password" : "text"}
          autoComplete="off"
          disabled={cleared}
          placeholder={
            cleared
              ? t("rapido.integrations.willClear")
              : field.kind === "intArray" || field.kind === "strArray"
              ? t("rapido.integrations.commaSeparated")
              : t("rapido.integrations.newValue")
          }
          value={draft}
          onChange={(e) => onDraftChange(e.target.value)}
        />
        <Button variant="chip" tone={cleared ? "amber" : "neutral"} onClick={onToggleClear}>
          {cleared ? t("cancel") : t("rapido.integrations.resetToEnv")}
        </Button>
      </div>
    </div>
  );
};

const IntegrationGroup: FC<{
  titleKey: string;
  enabled?: boolean;
  fields: FieldConfig[];
  status: IntegrationSettingsStatus;
  drafts: Record<string, string>;
  clearedKeys: Set<string>;
  onDraftChange: (key: string, v: string) => void;
  onToggleClear: (key: string) => void;
}> = ({ titleKey, enabled, fields, status, drafts, clearedKeys, onDraftChange, onToggleClear }) => {
  const { t } = useTranslation();
  return (
    <Card className="p-4">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-sm font-semibold">{t(titleKey)}</span>
        {enabled !== undefined && (
          <Badge tone={enabled ? "green" : "gray"}>
            {enabled ? t("rapido.integrations.enabled") : t("rapido.integrations.disabled")}
          </Badge>
        )}
      </div>
      {fields.map((f) => (
        <FieldRow
          key={f.key}
          field={f}
          status={status}
          draft={drafts[f.key] ?? ""}
          cleared={clearedKeys.has(f.key)}
          onDraftChange={(v) => onDraftChange(f.key, v)}
          onToggleClear={() => onToggleClear(f.key)}
        />
      ))}
    </Card>
  );
};

// ---------------------------------------------------------------------------

export const IntegrationsForm: FC = () => {
  const { t, i18n } = useTranslation();
  const [status, setStatus] = useState<IntegrationSettingsStatus | null>(null);
  const [loadError, setLoadError] = useState("");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [clearedKeys, setClearedKeys] = useState<Set<string>>(new Set());
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [saveOk, setSaveOk] = useState(false);

  const load = useCallback(() => {
    fetch<IntegrationSettingsStatus>("/settings/integrations")
      .then((r) => {
        setStatus(r);
        setLoadError("");
      })
      .catch((e) => setLoadError(errorText(e, t("rapido.integrations.loadFailed"))));
  }, [t]);

  useEffect(load, [load]);

  const onDraftChange = (key: string, v: string) => {
    setDrafts((d) => ({ ...d, [key]: v }));
    setSaveOk(false);
  };

  const onToggleClear = (key: string) => {
    setClearedKeys((s) => {
      const next = new Set(s);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
    setSaveOk(false);
  };

  const allFields = [...TELEGRAM_FIELDS, ...DISCORD_FIELDS, ...KIRBOT_FIELDS, ...WEBHOOK_FIELDS];

  const hasChanges =
    clearedKeys.size > 0 || allFields.some((f) => (drafts[f.key] ?? "").trim() !== "");

  const save = () => {
    setSaving(true);
    setSaveError("");
    setSaveOk(false);
    const body = buildIntegrationsPatch(allFields, drafts, clearedKeys);

    fetch<IntegrationSettingsStatus>("/settings/integrations", { method: "PUT", body })
      .then((r) => {
        setStatus(r);
        setDrafts({});
        setClearedKeys(new Set());
        setSaveOk(true);
      })
      .catch((e) => setSaveError(errorText(e, t("rapido.integrations.saveFailed"))))
      .finally(() => setSaving(false));
  };

  if (loadError) {
    return (
      <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
        {loadError}
      </div>
    );
  }

  if (!status) {
    return <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>;
  }

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-rapido-muted">
        {t("rapido.integrations.hint")}
        {status.updated_at && (
          <>
            {" · "}
            {t("rapido.integrations.lastUpdated")} {formatTime(i18n.language, status.updated_at)}
          </>
        )}
      </p>

      <IntegrationGroup
        titleKey="rapido.integrations.telegram"
        enabled={status.telegram_enabled}
        fields={TELEGRAM_FIELDS}
        status={status}
        drafts={drafts}
        clearedKeys={clearedKeys}
        onDraftChange={onDraftChange}
        onToggleClear={onToggleClear}
      />
      <p className="-mt-2 text-xs text-amber-400">{t("rapido.integrations.telegramRestartHint")}</p>

      <IntegrationGroup
        titleKey="rapido.integrations.discord"
        fields={DISCORD_FIELDS}
        status={status}
        drafts={drafts}
        clearedKeys={clearedKeys}
        onDraftChange={onDraftChange}
        onToggleClear={onToggleClear}
      />

      <IntegrationGroup
        titleKey="rapido.integrations.kirbot"
        enabled={status.kirbot_enabled}
        fields={KIRBOT_FIELDS}
        status={status}
        drafts={drafts}
        clearedKeys={clearedKeys}
        onDraftChange={onDraftChange}
        onToggleClear={onToggleClear}
      />

      <IntegrationGroup
        titleKey="rapido.integrations.webhook"
        fields={WEBHOOK_FIELDS}
        status={status}
        drafts={drafts}
        clearedKeys={clearedKeys}
        onDraftChange={onDraftChange}
        onToggleClear={onToggleClear}
      />

      {saveError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {saveError}
        </div>
      )}
      {saveOk && (
        <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-400">
          {t("rapido.integrations.saved")}
        </div>
      )}

      <div>
        <Button variant="chip" tone="accent" disabled={!hasChanges || saving} onClick={save}>
          {saving ? t("rapido.pleaseWait") : t("rapido.integrations.save")}
        </Button>
      </div>
    </div>
  );
};

export default IntegrationsForm;
