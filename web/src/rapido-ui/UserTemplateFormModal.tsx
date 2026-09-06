import { FC, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useInboundsQuery } from "hooks/useInboundsQuery";
import {
  useCreateUserTemplateMutation,
  useUpdateUserTemplateMutation,
} from "hooks/useUserTemplatesQuery";
import { UserTemplate, UserTemplateWritePayload } from "types/UserTemplate";
import { errorText } from "service/errors";
import { GIB, gbToDataLimit } from "utils/userConversions";
import { CardSubtitle, CardTitle } from "rapido-ui/Card";
import { Modal } from "rapido-ui/Modal";
import { Input } from "rapido-ui/Input";
import { Button } from "rapido-ui/Button";
import { InboundsPicker } from "rapido-ui/InboundsPicker";

export type UserTemplateFormModalProps = {
  /** null means "create a new template". */
  initial: UserTemplate | null;
  onClose: () => void;
};

// New feature area - the Python-era dashboard never had a UI for this either
// (see the plan's context: user_template's backend is a Phase 2 leftover
// nobody built a screen for). Deliberately simpler than UserFormModal.tsx in
// two ways the plan calls out explicitly:
//  - No "touched protocol" tracking: a template has no backend notion of
//    "every inbound of this protocol by default" the way a new user does, so
//    whatever is ticked here is exactly what gets submitted.
//  - data_limit/expire_duration carry none of User's null-means-unlimited
//    normalization (key fact #5) - both are plain, always-present int64
//    fields on the wire, so an empty field here means a literal 0, not null.
export const UserTemplateFormModal: FC<UserTemplateFormModalProps> = ({ initial, onClose }) => {
  const { t } = useTranslation();
  const isEdit = !!initial;

  const { data: inbounds } = useInboundsQuery();
  const createTemplate = useCreateUserTemplateMutation();
  const updateTemplate = useUpdateUserTemplateMutation();
  const saving = createTemplate.isPending || updateTemplate.isPending;

  const [name, setName] = useState(initial?.name ?? "");
  const [dataLimitGb, setDataLimitGb] = useState(
    initial ? String(initial.data_limit / GIB) : ""
  );
  const [expireDurationDays, setExpireDurationDays] = useState(
    initial ? String(initial.expire_duration) : ""
  );
  const [usernamePrefix, setUsernamePrefix] = useState(initial?.username_prefix ?? "");
  const [usernameSuffix, setUsernameSuffix] = useState(initial?.username_suffix ?? "");
  const [selectedInbounds, setSelectedInbounds] = useState<Record<string, Set<string>>>(() => {
    const initialState: Record<string, Set<string>> = {};
    Object.entries(initial?.inbounds ?? {}).forEach(([protocol, tags]) => {
      initialState[protocol] = new Set(tags);
    });
    return initialState;
  });
  const [selectedProtocols, setSelectedProtocols] = useState<Set<string>>(
    () => new Set(Object.keys(initial?.inbounds ?? {}))
  );
  const [error, setError] = useState("");

  const availableProtocols = useMemo(() => Object.keys(inbounds ?? {}).sort(), [inbounds]);
  // A template may reference a protocol whose inbounds have since been
  // removed from the server - shown (and kept selectable/clearable) the same
  // way UserFormModal.tsx handles an edited user's own "owned" protocols.
  const visibleProtocols = useMemo(
    () => Array.from(new Set([...availableProtocols, ...selectedProtocols])).sort(),
    [availableProtocols, selectedProtocols]
  );

  const toggleProtocol = (protocol: string) => {
    setSelectedProtocols((prev) => {
      const next = new Set(prev);
      if (next.has(protocol)) next.delete(protocol);
      else next.add(protocol);
      return next;
    });
  };

  const toggleTag = (protocol: string, tag: string) => {
    setSelectedInbounds((prev) => {
      const current = new Set(prev[protocol] ?? []);
      if (current.has(tag)) current.delete(tag);
      else current.add(tag);
      return { ...prev, [protocol]: current };
    });
  };

  const submit = () => {
    setError("");
    const gb = dataLimitGb ? Number(dataLimitGb) : 0;
    const days = expireDurationDays ? Number(expireDurationDays) : 0;

    const templateInbounds: Record<string, string[]> = {};
    selectedProtocols.forEach((protocol) => {
      templateInbounds[protocol] = Array.from(selectedInbounds[protocol] ?? []);
    });

    const body: UserTemplateWritePayload = {
      name,
      // gbToDataLimit returns null for a falsy GB (matching Users' own
      // "0/empty means unlimited" convention) - templates have no such
      // concept, so that null is coerced to a literal 0 here rather than
      // reused as-is.
      data_limit: gbToDataLimit(gb) ?? 0,
      expire_duration: days,
      username_prefix: usernamePrefix || null,
      username_suffix: usernameSuffix || null,
      inbounds: templateInbounds,
    };

    const request = isEdit
      ? updateTemplate.mutateAsync({ id: initial!.id, body })
      : createTemplate.mutateAsync(body);

    request.then(onClose).catch((e) => setError(errorText(e, t("rapido.templates.saveFailed"))));
  };

  const canSubmit = !!name.trim();

  return (
    <Modal onClose={onClose} className="max-w-2xl">
      <div className="mb-4">
        <CardTitle className="text-base">
          {isEdit ? t("rapido.templates.editTitle") : t("rapido.templates.addTitle")}
        </CardTitle>
        <CardSubtitle>{t("rapido.templates.notWiredHint")}</CardSubtitle>
      </div>

      <div className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.templates.name")}
            </label>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.templates.dataLimitGb")}
            </label>
            <Input
              type="number"
              min={0}
              step="any"
              value={dataLimitGb}
              onChange={(e) => setDataLimitGb(e.target.value)}
            />
            <p className="mt-1 text-xs text-rapido-muted">
              {t("rapido.templates.dataLimitGbHint")}
            </p>
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.templates.expireDurationDays")}
            </label>
            <Input
              type="number"
              min={0}
              step="1"
              value={expireDurationDays}
              onChange={(e) => setExpireDurationDays(e.target.value)}
            />
            <p className="mt-1 text-xs text-rapido-muted">
              {t("rapido.templates.expireDurationHint")}
            </p>
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.templates.usernamePrefix")}
            </label>
            <Input dir="ltr" value={usernamePrefix} onChange={(e) => setUsernamePrefix(e.target.value)} />
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.templates.usernameSuffix")}
            </label>
            <Input dir="ltr" value={usernameSuffix} onChange={(e) => setUsernameSuffix(e.target.value)} />
          </div>
        </div>

        <InboundsPicker
          protocols={visibleProtocols}
          isProtocolUnavailable={(protocol) => !availableProtocols.includes(protocol)}
          inboundsByProtocol={inbounds ?? {}}
          selectedProtocols={selectedProtocols}
          onToggleProtocol={toggleProtocol}
          selectedInbounds={selectedInbounds}
          onToggleTag={toggleTag}
        />

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t("cancel")}
          </Button>
          <Button variant="primary" disabled={saving || !canSubmit} onClick={submit}>
            {saving ? t("rapido.saving") : t("rapido.templates.save")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

export default UserTemplateFormModal;
