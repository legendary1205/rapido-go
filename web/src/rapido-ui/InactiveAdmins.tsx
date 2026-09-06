import { FC, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  usePreviewInactiveAdminsMutation,
  useDeleteInactiveAdminsMutation,
} from "hooks/useAdminsQuery";
import { InactiveAdminsDeleted, InactiveAdminsResult } from "types/Admin";
import { errorText } from "service/errors";
import { Card } from "rapido-ui/Card";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";

const formatTime = (locale: string, iso: string): string => {
  const d = new Date(/(?:Z|[+-]\d{2}:?\d{2})$/.test(iso) ? iso : `${iso}Z`);
  if (Number.isNaN(d.getTime())) return iso;
  try {
    return d.toLocaleString(locale, { dateStyle: "medium", timeStyle: "short" });
  } catch {
    return d.toLocaleString();
  }
};

export const InactiveAdmins: FC = () => {
  const { t, i18n } = useTranslation();
  const [days, setDays] = useState("90");
  const [result, setResult] = useState<InactiveAdminsResult | null>(null);
  const [deleted, setDeleted] = useState<InactiveAdminsDeleted | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState("");

  const preview = usePreviewInactiveAdminsMutation();
  const removeInactive = useDeleteInactiveAdminsMutation();

  const dayCount = Number(days);
  const canQuery = Number.isInteger(dayCount) && dayCount >= 1;

  const runPreview = () => {
    if (!canQuery) return;
    setError("");
    setDeleted(null);
    setConfirming(false);
    preview.mutate(dayCount, {
      onSuccess: (r) => setResult(r),
      onError: (e) => {
        setResult(null);
        setError(errorText(e, t("rapido.inactiveAdmins.loadFailed")));
      },
    });
  };

  const remove = () => {
    if (!result) return;
    setError("");
    removeInactive.mutate(result.cutoff_days, {
      onSuccess: (r) => {
        setDeleted(r);
        setResult(null);
        setConfirming(false);
      },
      onError: (e) => {
        setError(errorText(e, t("rapido.inactiveAdmins.deleteFailed")));
        setConfirming(false);
      },
    });
  };

  return (
    <Card className="p-4">
      <div className="mb-1 text-sm font-semibold">{t("rapido.inactiveAdmins.title")}</div>
      <p className="mb-3 text-xs text-rapido-muted">{t("rapido.inactiveAdmins.subtitle")}</p>

      <div className="flex flex-wrap items-center gap-2">
        <label className="flex items-center gap-2 text-sm">
          <span className="text-xs text-rapido-muted">{t("rapido.inactiveAdmins.days")}</span>
          <Input
            className="w-24"
            dir="ltr"
            inputMode="numeric"
            value={days}
            onChange={(e) => setDays(e.target.value.replace(/[^0-9]/g, ""))}
          />
        </label>
        <Button
          variant="chip"
          tone="accent"
          disabled={!canQuery || preview.isPending}
          onClick={runPreview}
        >
          {preview.isPending ? t("rapido.pleaseWait") : t("rapido.inactiveAdmins.preview")}
        </Button>
      </div>

      {error && (
        <div className="mt-3 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          {error}
        </div>
      )}

      {deleted && (
        <div className="mt-3 rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-400">
          {t("rapido.inactiveAdmins.deletedSummary", {
            admins: deleted.admins.length,
            users: deleted.users_removed,
          })}
        </div>
      )}

      {result && (
        <div className="mt-3 flex flex-col gap-2">
          {result.admins.length === 0 ? (
            <p className="text-sm text-rapido-muted">{t("rapido.inactiveAdmins.empty")}</p>
          ) : (
            <>
              <div className="overflow-x-auto rounded-lg border border-rapido-border">
                <table className="w-full text-start text-xs">
                  <thead className="bg-white/5 text-rapido-muted">
                    <tr>
                      <th className="px-3 py-2 text-start font-medium">{t("username")}</th>
                      <th className="px-3 py-2 text-start font-medium">
                        {t("rapido.inactiveAdmins.lastActivity")}
                      </th>
                      <th className="px-3 py-2 text-start font-medium">
                        {t("rapido.inactiveAdmins.userCount")}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {result.admins.map((a) => (
                      <tr key={a.username} className="border-t border-rapido-border">
                        <td className="px-3 py-2" dir="ltr">
                          {a.username}
                        </td>
                        <td className="px-3 py-2 text-rapido-muted">
                          {formatTime(i18n.language, a.last_activity)}
                        </td>
                        <td className="px-3 py-2 text-rapido-muted">{a.user_count}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              <div className="flex flex-wrap items-center gap-2">
                {confirming ? (
                  <>
                    <span className="text-xs text-red-400">
                      {t("rapido.inactiveAdmins.deleteWarning", { count: result.admins.length })}
                    </span>
                    <Button
                      variant="chip"
                      tone="red"
                      disabled={removeInactive.isPending}
                      onClick={remove}
                    >
                      {removeInactive.isPending
                        ? t("rapido.pleaseWait")
                        : t("rapido.inactiveAdmins.deleteConfirm")}
                    </Button>
                    <Button variant="chip" onClick={() => setConfirming(false)}>
                      {t("cancel")}
                    </Button>
                  </>
                ) : (
                  <Button variant="chip" tone="amber" onClick={() => setConfirming(true)}>
                    {t("rapido.inactiveAdmins.deleteAll", { count: result.admins.length })}
                  </Button>
                )}
              </div>
            </>
          )}
        </div>
      )}
    </Card>
  );
};

export default InactiveAdmins;
