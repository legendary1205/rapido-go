import { FC, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import {
  useAdminsQuery,
  useCreateAdminMutation,
  useDeleteAdminMutation,
  useUpdateAdminMutation,
} from "hooks/useAdminsQuery";
import { useCurrentAdminQuery } from "hooks/useCurrentAdminQuery";
import { Admin } from "types/Admin";
import { errorText } from "service/errors";
import { Card } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";
import { Checkbox } from "rapido-ui/Checkbox";
import { Modal } from "rapido-ui/Modal";
import { formatBytes } from "utils/formatByte";

// Per the plan's key fact #4: the old dashboard's per-admin "activate users",
// "disable users" and "reset usage counter" buttons have no Go backend at
// all (not even a query for the first two) - removed here rather than wired
// to a 404, leaving Edit/Delete and the InactiveAdmins card as this phase's
// full admin-management surface. A fast-follow, not a silent regression.

// ---------------------------------------------------------------------------

const AdminForm: FC<{
  initial: Admin | null;
  onClose: () => void;
}> = ({ initial, onClose }) => {
  const { t } = useTranslation();
  const isEdit = !!initial;
  const [username, setUsername] = useState(initial?.username ?? "");
  const [password, setPassword] = useState("");
  const [isSudo, setIsSudo] = useState(initial?.is_sudo ?? false);
  const [telegramId, setTelegramId] = useState(
    initial?.telegram_id != null ? String(initial.telegram_id) : ""
  );
  const [discord, setDiscord] = useState(initial?.discord_webhook ?? "");
  const [error, setError] = useState("");

  const createAdmin = useCreateAdminMutation();
  const updateAdmin = useUpdateAdminMutation();
  const saving = createAdmin.isPending || updateAdmin.isPending;

  const submit = () => {
    setError("");
    const request = isEdit
      ? updateAdmin.mutateAsync({
          username: initial!.username,
          body: {
            is_sudo: isSudo,
            telegram_id: telegramId ? Number(telegramId) : null,
            // Left blank on edit means "keep the current password" - sending
            // an empty string would hash it and lock the admin out.
            password: password || undefined,
            discord_webhook: discord || null,
          },
        })
      : createAdmin.mutateAsync({
          username,
          password,
          is_sudo: isSudo,
          telegram_id: telegramId ? Number(telegramId) : null,
          discord_webhook: discord || null,
        });

    request.then(onClose).catch((e) => setError(errorText(e, t("rapido.admins.saveFailed"))));
  };

  const canSubmit = isEdit ? true : !!username && !!password;

  return (
    <Modal onClose={onClose} className="max-w-md">
      <h2 className="mb-4 text-lg font-semibold">
        {isEdit ? t("rapido.admins.editTitle") : t("rapido.admins.addTitle")}
      </h2>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("username")}</span>
          <Input
            dir="ltr"
            value={username}
            disabled={isEdit}
            onChange={(e) => setUsername(e.target.value)}
          />
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">
            {t("password")}
            {isEdit ? ` — ${t("rapido.admins.passwordKeepHint")}` : ""}
          </span>
          <Input
            dir="ltr"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>

        <Checkbox
          checked={isSudo}
          onChange={(e) => setIsSudo(e.target.checked)}
          label={t("rapido.admins.isSudo")}
        />
        {isSudo && (
          <p className="-mt-1 text-xs text-amber-400">{t("rapido.admins.sudoWarning")}</p>
        )}

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.admins.telegramId")}</span>
          <Input
            dir="ltr"
            inputMode="numeric"
            value={telegramId}
            onChange={(e) => setTelegramId(e.target.value.replace(/[^0-9-]/g, ""))}
          />
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.admins.discordWebhook")}</span>
          <Input
            dir="ltr"
            value={discord}
            onChange={(e) => setDiscord(e.target.value)}
            placeholder="https://discord.com/..."
          />
        </label>

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button variant="chip" tone="accent" disabled={saving || !canSubmit} onClick={submit}>
            {saving ? t("rapido.pleaseWait") : t("rapido.admins.save")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

// ---------------------------------------------------------------------------

const AdminCard: FC<{
  row: Admin;
  isSelf: boolean;
  onEdit: () => void;
}> = ({ row, isSelf, onEdit }) => {
  const { t } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const deleteAdmin = useDeleteAdminMutation();

  const remove = () => {
    setMsg(null);
    deleteAdmin.mutate(row.username, {
      onSuccess: () => setMsg({ tone: "ok", text: t("rapido.admins.deleted") }),
      onError: (e) => {
        setMsg({ tone: "err", text: errorText(e, t("rapido.admins.actionFailed")) });
        setConfirmDelete(false);
      },
    });
  };

  return (
    <Card
      className={classNames(
        "p-4",
        row.is_sudo
          ? "!border-rapido-accent/60 bg-rapido-accent/[0.04]"
          : "!border-sky-500/60 bg-sky-500/[0.04]"
      )}
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="truncate text-sm font-semibold" dir="ltr">
              {row.username}
            </span>
            <Badge tone={row.is_sudo ? "brand" : "sky"}>
              {row.is_sudo ? t("rapido.admins.sudo") : t("rapido.admins.reseller")}
            </Badge>
            {isSelf && <Badge tone="gray">{t("rapido.admins.you")}</Badge>}
          </div>
          <span className="text-xs text-rapido-muted">
            {t("rapido.admins.usage")}{" "}
            <span className="tabular-nums text-rapido-text" dir="ltr">
              {formatBytes(row.users_usage || 0)}
            </span>
          </span>
        </div>

        {(row.telegram_id || row.discord_webhook) && (
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-rapido-muted">
            {row.telegram_id ? (
              <span>
                Telegram: <span className="text-rapido-text" dir="ltr">{row.telegram_id}</span>
              </span>
            ) : null}
            {row.discord_webhook ? <span>Discord ✓</span> : null}
          </div>
        )}

        {msg && (
          <div
            className={classNames(
              "rounded-lg border px-3 py-2 text-xs",
              msg.tone === "ok"
                ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                : "border-red-500/30 bg-red-500/10 text-red-400"
            )}
          >
            {msg.text}
          </div>
        )}

        <div className="flex flex-wrap items-center gap-1.5">
          <Button variant="chip" tone="accent" onClick={onEdit}>
            {t("rapido.edit")}
          </Button>

          {/* Deleting yourself would end the session you are working in, and
              deleting an admin orphans their customers - both get a guard. */}
          {!isSelf &&
            (confirmDelete ? (
              <>
                <span className="text-xs text-red-400">{t("rapido.admins.deleteWarning")}</span>
                <Button variant="chip" tone="red" disabled={deleteAdmin.isPending} onClick={remove}>
                  {t("delete")}
                </Button>
                <Button variant="chip" onClick={() => setConfirmDelete(false)}>
                  {t("cancel")}
                </Button>
              </>
            ) : (
              <Button variant="chip" tone="red" onClick={() => setConfirmDelete(true)}>
                {t("delete")}
              </Button>
            ))}
        </div>
      </div>
    </Card>
  );
};

// ---------------------------------------------------------------------------

export const AdminsAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: currentAdmin } = useCurrentAdminQuery();
  const { data: rows, isLoading, isError } = useAdminsQuery();
  const [editing, setEditing] = useState<Admin | null | undefined>(undefined);

  const sudoCount = (rows ?? []).filter((r) => r.is_sudo).length;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">
          {t("rapido.admins.summary", { total: rows?.length ?? 0, sudo: sudoCount })}
        </div>
        <Button variant="chip" tone="accent" onClick={() => setEditing(null)}>
          + {t("rapido.admins.addTitle")}
        </Button>
      </div>

      {isError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {t("rapido.admins.loadFailed")}
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : (rows ?? []).length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">
          {t("rapido.admins.empty")}
        </Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {(rows ?? []).map((row) => (
            <AdminCard
              key={row.username}
              row={row}
              isSelf={row.username === currentAdmin?.username}
              onEdit={() => setEditing(row)}
            />
          ))}
        </div>
      )}

      {editing !== undefined && (
        <AdminForm initial={editing} onClose={() => setEditing(undefined)} />
      )}
    </div>
  );
};

export default AdminsAdmin;
