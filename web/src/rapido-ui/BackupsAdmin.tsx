import { FC, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  downloadBackup,
  useBackupsQuery,
  useCreateBackupMutation,
  useDeleteBackupMutation,
} from "hooks/useBackupsQuery";
import { Backup } from "types/Backup";
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
const BackupRow: FC<{ backup: Backup }> = ({ backup }) => {
  const { t, i18n } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [downloadFailed, setDownloadFailed] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const deleteBackup = useDeleteBackupMutation();

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

export const BackupsAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: backups, isLoading, isError } = useBackupsQuery();
  const createBackup = useCreateBackupMutation();
  const [createError, setCreateError] = useState("");

  const rows = backups ?? [];

  const create = () => {
    setCreateError("");
    createBackup.mutate(undefined, {
      onError: (e) => setCreateError(errorText(e, t("rapido.backups.createFailed"))),
    });
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

      {isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : rows.length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">{t("rapido.backups.empty")}</Card>
      ) : (
        <div className="flex flex-col gap-2">
          {rows.map((b) => (
            <BackupRow key={b.filename} backup={b} />
          ))}
        </div>
      )}
    </div>
  );
};

export default BackupsAdmin;
