import { ChangeEvent, FC, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  downloadBackup,
  useBackupsQuery,
  useCreateBackupMutation,
  useDeleteBackupMutation,
  useRestoreBackupMutation,
  useRestoreUploadMutation,
} from "hooks/useBackupsQuery";
import { Backup, RestoreResult } from "types/Backup";
import { errorText } from "service/errors";
import { formatBytes } from "utils/formatByte";
import { absoluteTime } from "utils/ticketHelpers";
import { Card } from "rapido-ui/Card";
import { Button } from "rapido-ui/Button";

// A gzipped pg_dump of the whole panel database, taken on demand and kept
// on disk (no DB table - see internal/httpapi/backup.go's own doc comment,
// a filename this backend generated is the identity). Deliberately its own
// top-level page rather than a card bolted onto Integrations, matching how
// Nodes/Monitoring/Core Config/Tickets each got their own page in this
// rewrite rather than being bundled together.
const BackupRow: FC<{
  backup: Backup;
  onRestored: (result: RestoreResult | null, error: string | null) => void;
}> = ({ backup, onRestored }) => {
  const { t, i18n } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmRestore, setConfirmRestore] = useState(false);
  const [downloadFailed, setDownloadFailed] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const deleteBackup = useDeleteBackupMutation();
  const restoreBackup = useRestoreBackupMutation();

  const download = () => {
    setDownloadFailed(false);
    downloadBackup(backup.filename).catch(() => setDownloadFailed(true));
  };

  const remove = () => {
    setDeleteError("");
    deleteBackup.mutate(backup.filename, {
      onError: (e) => {
        setDeleteError(errorText(e, t("rapido.backups.deleteFailed")));
        setConfirmDelete(false);
      },
    });
  };

  const restore = () => {
    setConfirmRestore(false);
    restoreBackup.mutate(backup.filename, {
      onSuccess: (result) => onRestored(result, null),
      onError: (e) => onRestored(null, errorText(e, t("rapido.backups.restoreFailed"))),
    });
  };

  return (
    <Card className="flex flex-col gap-2 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="truncate font-mono text-sm" dir="ltr">
            {backup.filename}
          </span>
          <span className="text-xs text-rapido-muted">
            {formatBytes(backup.size_bytes)} ·{" "}
            <span dir="ltr">{absoluteTime(i18n.language, backup.created_at)}</span>
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <Button variant="chip" tone="accent" onClick={download}>
            {t("rapido.backups.download")}
          </Button>
          {confirmRestore ? (
            <>
              <span className="text-xs text-red-400">{t("rapido.backups.restoreConfirm")}</span>
              <Button
                variant="chip"
                tone="red"
                disabled={restoreBackup.isPending}
                onClick={restore}
              >
                {restoreBackup.isPending ? t("rapido.pleaseWait") : t("rapido.backups.restoreConfirmButton")}
              </Button>
              <Button variant="chip" onClick={() => setConfirmRestore(false)}>
                {t("cancel")}
              </Button>
            </>
          ) : (
            <Button variant="chip" tone="amber" onClick={() => setConfirmRestore(true)}>
              {t("rapido.backups.restore")}
            </Button>
          )}
          {confirmDelete ? (
            <>
              <span className="text-xs text-red-400">{t("rapido.backups.deleteConfirm")}</span>
              <Button variant="chip" tone="red" disabled={deleteBackup.isPending} onClick={remove}>
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
          )}
        </div>
      </div>

      {downloadFailed && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          {t("rapido.backups.downloadFailed")}
        </div>
      )}
      {deleteError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          {deleteError}
        </div>
      )}
    </Card>
  );
};

// UploadRestoreCard is collapsed by default (matches the Core Config JSON
// card's/Hosts variables reference's Show/Hide convention elsewhere in this
// dashboard) - restoring from an arbitrary uploaded file is a rarer,
// higher-stakes action than the per-row Restore button, so it shouldn't
// compete for attention with the normal backup list.
const UploadRestoreCard: FC<{
  onRestored: (result: RestoreResult | null, error: string | null) => void;
}> = ({ onRestored }) => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [confirming, setConfirming] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const restoreUpload = useRestoreUploadMutation();

  const onFileChange = (e: ChangeEvent<HTMLInputElement>) => {
    setFile(e.target.files?.[0] ?? null);
    setConfirming(false);
  };

  const restore = () => {
    if (!file) return;
    setConfirming(false);
    restoreUpload.mutate(file, {
      onSuccess: (result) => {
        onRestored(result, null);
        setFile(null);
        if (fileInputRef.current) fileInputRef.current.value = "";
      },
      onError: (e) => onRestored(null, errorText(e, t("rapido.backups.restoreFailed"))),
    });
  };

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-sm font-semibold">{t("rapido.backups.uploadTitle")}</div>
          <div className="text-xs text-rapido-muted">{t("rapido.backups.uploadDesc")}</div>
        </div>
        <Button variant="chip" onClick={() => setOpen((o) => !o)}>
          {open ? t("rapido.backups.hideUpload") : t("rapido.backups.showUpload")}
        </Button>
      </div>

      {open && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <input
            ref={fileInputRef}
            type="file"
            accept=".gz"
            onChange={onFileChange}
            className="text-xs text-rapido-muted file:mr-2 file:rounded-md file:border file:border-rapido-border file:bg-rapido-bg file:px-2.5 file:py-1 file:text-xs file:text-rapido-text"
          />
          {confirming ? (
            <>
              <span className="text-xs text-red-400">{t("rapido.backups.restoreConfirm")}</span>
              <Button variant="chip" tone="red" disabled={restoreUpload.isPending} onClick={restore}>
                {restoreUpload.isPending ? t("rapido.pleaseWait") : t("rapido.backups.restoreConfirmButton")}
              </Button>
              <Button variant="chip" onClick={() => setConfirming(false)}>
                {t("cancel")}
              </Button>
            </>
          ) : (
            <Button
              variant="chip"
              tone="amber"
              disabled={!file}
              onClick={() => setConfirming(true)}
            >
              {t("rapido.backups.restore")}
            </Button>
          )}
        </div>
      )}
    </Card>
  );
};

export const BackupsAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: backups, isLoading, isError } = useBackupsQuery();
  const createBackup = useCreateBackupMutation();
  const [createError, setCreateError] = useState("");
  const [restoreMsg, setRestoreMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);

  const rows = backups ?? [];

  const create = () => {
    setCreateError("");
    createBackup.mutate(undefined, {
      onError: (e) => setCreateError(errorText(e, t("rapido.backups.createFailed"))),
    });
  };

  // Shared by both the per-row Restore button and the upload-and-restore
  // card - either path ends the same way: a page-level banner naming the
  // safety backup, since the row/section that triggered it may itself
  // reorder or disappear once the backup list refetches.
  const handleRestored = (result: RestoreResult | null, error: string | null) => {
    setRestoreMsg(
      result
        ? { tone: "ok", text: t("rapido.backups.restoreSuccess", { filename: result.safety_backup.filename }) }
        : { tone: "err", text: error || t("rapido.backups.restoreFailed") }
    );
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">{t("rapido.backups.retentionHint")}</div>
        <Button variant="chip" tone="accent" disabled={createBackup.isPending} onClick={create}>
          {createBackup.isPending ? t("rapido.backups.creating") : t("rapido.backups.createButton")}
        </Button>
      </div>

      {createError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {createError}
        </div>
      )}
      {isError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {t("rapido.backups.loadFailed")}
        </div>
      )}
      {restoreMsg && (
        <div
          className={
            restoreMsg.tone === "ok"
              ? "rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-400"
              : "rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400"
          }
        >
          {restoreMsg.text}
        </div>
      )}

      <UploadRestoreCard onRestored={handleRestored} />

      {isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : rows.length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">{t("rapido.backups.empty")}</Card>
      ) : (
        <div className="flex flex-col gap-2">
          {rows.map((b) => (
            <BackupRow key={b.filename} backup={b} onRestored={handleRestored} />
          ))}
        </div>
      )}
    </div>
  );
};

export default BackupsAdmin;
