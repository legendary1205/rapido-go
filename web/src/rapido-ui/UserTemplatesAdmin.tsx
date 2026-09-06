import { FC, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCurrentAdminQuery } from "hooks/useCurrentAdminQuery";
import {
  useDeleteUserTemplateMutation,
  useUserTemplatesQuery,
} from "hooks/useUserTemplatesQuery";
import { UserTemplate } from "types/UserTemplate";
import { errorText } from "service/errors";
import { formatBytes } from "utils/formatByte";
import { Card } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { UserTemplateFormModal } from "rapido-ui/UserTemplateFormModal";

// Same list-of-cards + modal shape as AdminsAdmin.tsx/HostsAdmin.tsx (per the
// plan's own note that this brand-new page should match that pattern
// exactly). Every admin can view templates (GET /api/user_template is
// requireAdmin), but only sudo can create/edit/delete them (the write
// endpoints are requireSudo) - the Add/Edit/Delete controls are hidden for a
// non-sudo admin rather than shown and left to fail with a 403.
const TemplateCard: FC<{
  template: UserTemplate;
  canWrite: boolean;
  onEdit: () => void;
}> = ({ template, canWrite, onEdit }) => {
  const { t } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const deleteTemplate = useDeleteUserTemplateMutation();

  const remove = () => {
    setMsg(null);
    deleteTemplate.mutate(template.id, {
      onError: (e) => {
        setMsg({ tone: "err", text: errorText(e, t("rapido.templates.deleteFailed")) });
        setConfirmDelete(false);
      },
    });
  };

  const protocols = Object.keys(template.inbounds);

  return (
    <Card className="p-4">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="truncate text-sm font-semibold" dir="ltr">
            {template.name}
          </span>
          <div className="flex flex-wrap items-center gap-1.5">
            {protocols.map((protocol) => (
              <Badge key={protocol} tone="brand">
                {protocol}
              </Badge>
            ))}
          </div>
        </div>

        <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-rapido-muted">
          <span>
            {t("rapido.templates.dataLimitGb")}:{" "}
            <span className="tabular-nums text-rapido-text" dir="ltr">
              {formatBytes(template.data_limit)}
            </span>
          </span>
          <span>
            {t("rapido.templates.expireDurationDays")}:{" "}
            <span className="tabular-nums text-rapido-text" dir="ltr">
              {template.expire_duration}
            </span>
          </span>
          {(template.username_prefix || template.username_suffix) && (
            <span dir="ltr">
              {template.username_prefix ?? ""}
              <span className="text-rapido-text/50">{"{username}"}</span>
              {template.username_suffix ?? ""}
            </span>
          )}
        </div>

        {msg && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
            {msg.text}
          </div>
        )}

        {canWrite && (
          <div className="flex flex-wrap items-center gap-1.5">
            <Button variant="chip" tone="accent" onClick={onEdit}>
              {t("rapido.edit")}
            </Button>
            {confirmDelete ? (
              <>
                <span className="text-xs text-red-400">{t("rapido.templates.deleteWarning")}</span>
                <Button variant="chip" tone="red" disabled={deleteTemplate.isPending} onClick={remove}>
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
        )}
      </div>
    </Card>
  );
};

export const UserTemplatesAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: currentAdmin } = useCurrentAdminQuery();
  const { data: templates, isLoading, isError } = useUserTemplatesQuery();
  const [editing, setEditing] = useState<UserTemplate | null | undefined>(undefined);

  const canWrite = !!currentAdmin?.is_sudo;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">
          {t("rapido.templates.summary", { total: templates?.length ?? 0 })}
        </div>
        {canWrite && (
          <Button variant="chip" tone="accent" onClick={() => setEditing(null)}>
            + {t("rapido.templates.addTitle")}
          </Button>
        )}
      </div>

      {isError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {t("rapido.templates.loadFailed")}
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : (templates ?? []).length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">
          {t("rapido.templates.empty")}
        </Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {(templates ?? []).map((template) => (
            <TemplateCard
              key={template.id}
              template={template}
              canWrite={canWrite}
              onEdit={() => setEditing(template)}
            />
          ))}
        </div>
      )}

      {editing !== undefined && (
        <UserTemplateFormModal initial={editing} onClose={() => setEditing(undefined)} />
      )}
    </div>
  );
};

export default UserTemplatesAdmin;
