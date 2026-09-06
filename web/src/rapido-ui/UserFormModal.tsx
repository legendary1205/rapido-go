import {
  FC,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useInboundsQuery } from "hooks/useInboundsQuery";
import { useCreateUserMutation, useUpdateUserMutation } from "hooks/useUsersQuery";
import { useUsersUiStore } from "rapido-ui/usersUiStore";
import { errorText } from "service/errors";
import {
  DataLimitResetStrategy,
  ProtocolType,
  ProxySettingsMap,
  Status,
  UserCreatePayload,
  UserInbounds,
} from "types/User";
import {
  daysToExpire,
  dataLimitToGb,
  expireToDays,
  gbToDataLimit,
} from "utils/userConversions";
import { CardSubtitle, CardTitle } from "rapido-ui/Card";
import { Modal } from "rapido-ui/Modal";
import { Input } from "rapido-ui/Input";
import { Select } from "rapido-ui/Select";
import { Button } from "rapido-ui/Button";
import { InboundsPicker } from "rapido-ui/InboundsPicker";

// Display order only - the set of protocols actually offered comes from
// GET /api/inbounds, which reflects this server's inbound config.
const PROTOCOL_ORDER: ProtocolType[] = [
  "vmess",
  "vless",
  "trojan",
  "shadowsocks",
];

const STATUSES: Status[] = ["active", "on_hold", "limited", "expired", "disabled"];

const RESET_STRATEGIES: { value: DataLimitResetStrategy; labelKey: string }[] = [
  { value: "no_reset", labelKey: "userDialog.resetStrategyNo" },
  { value: "day", labelKey: "userDialog.resetStrategyDaily" },
  { value: "week", labelKey: "userDialog.resetStrategyWeekly" },
  { value: "month", labelKey: "userDialog.resetStrategyMonthly" },
  { value: "year", labelKey: "userDialog.resetStrategyAnnually" },
];

const formSchema = z.object({
  username: z
    .string()
    .min(1, "Username is required")
    .refine((v) => !/\s/.test(v), "Username cannot contain spaces"),
  status: z.string(),
  data_limit_gb: z.string(),
  data_limit_reset_strategy: z.string(),
  expire_days: z.string(),
  note: z.string(),
});

type FormValues = z.infer<typeof formSchema>;

const emptyFormValues: FormValues = {
  username: "",
  status: "active",
  data_limit_gb: "",
  data_limit_reset_strategy: "no_reset",
  expire_days: "",
  note: "",
};

const sortByProtocolOrder = (protocols: string[]): string[] => {
  const rank = (protocol: string) => {
    const index = PROTOCOL_ORDER.indexOf(protocol as ProtocolType);
    return index === -1 ? PROTOCOL_ORDER.length : index;
  };
  return [...protocols].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
};

export const UserFormModal: FC = () => {
  const { t } = useTranslation();
  const isCreatingNewUser = useUsersUiStore((s) => s.isCreatingNewUser);
  const editingUser = useUsersUiStore((s) => s.editingUser);
  const setCreatingNewUser = useUsersUiStore((s) => s.setCreatingNewUser);
  const setEditingUser = useUsersUiStore((s) => s.setEditingUser);

  const { data: inbounds } = useInboundsQuery();
  const createUser = useCreateUserMutation();
  const updateUser = useUpdateUserMutation();

  const isEdit = !!editingUser;
  const isOpen = isCreatingNewUser || isEdit;

  const [error, setError] = useState("");
  const [selectedProtocols, setSelectedProtocols] = useState<Set<string>>(new Set());
  const [selectedInbounds, setSelectedInbounds] = useState<Record<string, Set<string>>>({});
  // Protocols whose inbound checkboxes have an explicit selection (either
  // pre-filled from an existing user, or touched by the admin) - only these
  // are submitted, so untouched protocols fall back to the backend's "all
  // inbounds for this protocol" default instead of an empty list.
  const [touchedInboundProtocols, setTouchedInboundProtocols] = useState<Set<string>>(new Set());
  // Inbounds arrive asynchronously, so the create form's "select everything
  // available" default may only become possible after the modal is open.
  // This marks it as already applied so a later refetch cannot silently undo
  // the admin's own checkbox changes.
  const createDefaultsApplied = useRef(false);

  const availableProtocols = useMemo(
    () => sortByProtocolOrder(Object.keys(inbounds ?? {})),
    [inbounds]
  );

  const ownedProtocols = useMemo(
    () => (editingUser ? Object.keys(editingUser.proxies) : []),
    [editingUser]
  );

  // A user may still own a protocol that has since been dropped from the
  // inbound config. Hiding it would submit `proxies` without it and make the
  // backend delete it from the user, so edit mode shows the union and keeps
  // it ticked.
  const visibleProtocols = useMemo(
    () => sortByProtocolOrder(Array.from(new Set([...availableProtocols, ...ownedProtocols]))),
    [availableProtocols, ownedProtocols]
  );

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: emptyFormValues,
  });

  useEffect(() => {
    if (!isOpen) return;
    setError("");
    if (editingUser) {
      reset({
        username: editingUser.username,
        status: editingUser.status,
        data_limit_gb: dataLimitToGb(editingUser.data_limit)?.toString() ?? "",
        data_limit_reset_strategy: editingUser.data_limit_reset_strategy,
        expire_days: expireToDays(editingUser.expire)?.toString() ?? "",
        note: editingUser.note ?? "",
      });
      setSelectedProtocols(new Set(Object.keys(editingUser.proxies)));
      const inboundState: Record<string, Set<string>> = {};
      const touched = new Set<string>();
      Object.entries(editingUser.inbounds ?? {}).forEach(([protocol, tags]) => {
        inboundState[protocol] = new Set(tags);
        touched.add(protocol);
      });
      setSelectedInbounds(inboundState);
      setTouchedInboundProtocols(touched);
    } else {
      reset(emptyFormValues);
      createDefaultsApplied.current = false;
      setSelectedProtocols(new Set());
      setSelectedInbounds({});
      setTouchedInboundProtocols(new Set());
    }
    // Keyed on the target user's identity, not the editingUser object
    // reference, so a background refresh of the same user (e.g. after
    // revoking their subscription) doesn't silently wipe in-progress edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOpen, editingUser?.username]);

  useEffect(() => {
    if (!isOpen || isEdit) return;
    if (createDefaultsApplied.current || availableProtocols.length === 0) return;
    createDefaultsApplied.current = true;
    setSelectedProtocols(new Set(availableProtocols));
    // Tick every inbound too. Leaving them visually unchecked would tell the
    // admin the new user gets no servers, when the backend actually grants
    // every inbound of a protocol whose list isn't specified - left
    // untouched (not marked in touchedInboundProtocols), so the payload
    // still omits the list and the backend keeps applying that default.
    const allTags: Record<string, Set<string>> = {};
    availableProtocols.forEach((protocol) => {
      allTags[protocol] = new Set(inbounds?.[protocol] ?? []);
    });
    setSelectedInbounds(allTags);
  }, [isOpen, isEdit, availableProtocols, inbounds]);

  const usernameError = errors.username?.message ? t(errors.username.message as string) : "";

  if (!isOpen) return null;

  const submitting = createUser.isPending || updateUser.isPending;

  const close = () => {
    if (submitting) return;
    if (isEdit) setEditingUser(null);
    else setCreatingNewUser(false);
  };

  const toggleProtocol = (protocol: string) => {
    setSelectedProtocols((prev) => {
      const next = new Set(prev);
      if (next.has(protocol)) {
        next.delete(protocol);
      } else {
        next.add(protocol);
        setSelectedInbounds((current) =>
          current[protocol]
            ? current
            : { ...current, [protocol]: new Set(inbounds?.[protocol] ?? []) }
        );
      }
      return next;
    });
  };

  const toggleInboundTag = (protocol: string, tag: string) => {
    setTouchedInboundProtocols((prev) => new Set(prev).add(protocol));
    setSelectedInbounds((prev) => {
      const current = new Set(prev[protocol] ?? []);
      if (current.has(tag)) current.delete(tag);
      else current.add(tag);
      return { ...prev, [protocol]: current };
    });
  };

  const onSubmit = (values: FormValues) => {
    setError("");

    const days = values.expire_days ? Number(values.expire_days) : 0;
    const gb = values.data_limit_gb ? Number(values.data_limit_gb) : 0;
    const data_limit = gbToDataLimit(gb);

    const proxies: ProxySettingsMap = {};
    selectedProtocols.forEach((protocol) => {
      proxies[protocol] = {};
    });

    const userInbounds: UserInbounds = {};
    selectedProtocols.forEach((protocol) => {
      if (touchedInboundProtocols.has(protocol)) {
        userInbounds[protocol] = Array.from(selectedInbounds[protocol] ?? []);
      }
    });

    const payload: UserCreatePayload = {
      username: values.username,
      status: isEdit ? (values.status as Status) : "active",
      data_limit,
      // No point scheduling a reset for a limit that doesn't exist.
      data_limit_reset_strategy: data_limit
        ? (values.data_limit_reset_strategy as DataLimitResetStrategy)
        : "no_reset",
      expire: daysToExpire(days),
      note: values.note,
      on_hold_expire_duration: null,
      proxies,
      inbounds: userInbounds,
    };

    const request = isEdit
      ? updateUser.mutateAsync({ username: payload.username, body: payload })
      : createUser.mutateAsync(payload);

    request
      .then(() => {
        if (isEdit) setEditingUser(null);
        else setCreatingNewUser(false);
      })
      .catch((err: unknown) => {
        setError(errorText(err, t("rapido.failedToSaveUser")));
      });
  };

  // A request carrying zero proxies is always rejected, so block the submit
  // rather than let the admin discover it through a 422.
  const canSubmit = selectedProtocols.size > 0;

  return (
    <Modal onClose={close} className="max-w-2xl">
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <CardTitle className="text-base">
            {isEdit ? t("userDialog.editUserTitle") : t("createNewUser")}
          </CardTitle>
          <CardSubtitle>
            {isEdit ? t("rapido.editUserDesc") : t("rapido.createUserDesc")}
          </CardSubtitle>
        </div>
        <button
          type="button"
          onClick={close}
          disabled={submitting}
          className="shrink-0 rounded-lg px-2 py-1 text-rapido-muted hover:bg-rapido-raised hover:text-rapido-text disabled:cursor-not-allowed disabled:opacity-50"
          aria-label={t("rapido.close")}
        >
          ×
        </button>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("username")}
            </label>
            <Input
              {...register("username")}
              disabled={isEdit}
              autoComplete="off"
              // Usernames are ASCII and the field rejects spaces; typing one
              // into an RTL-inheriting box put the caret on the wrong side.
              dir="ltr"
              hasError={!!errors.username}
              className="text-start"
            />
            {usernameError && (
              <p className="mt-1 text-xs text-red-400">{usernameError}</p>
            )}
          </div>

          {isEdit && (
            <div>
              <label className="mb-1 block text-xs font-medium text-rapido-muted">
                {t("rapido.statusLabel")}
              </label>
              <Select {...register("status")}>
                {STATUSES.map((status) => (
                  <option key={status} value={status}>
                    {t(`status.${status}`)}
                  </option>
                ))}
              </Select>
            </div>
          )}

          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.dataLimitGb")}
            </label>
            <Input type="number" min={0} step="any" {...register("data_limit_gb")} />
            <p className="mt-1 text-xs text-rapido-muted">{t("rapido.zeroUnlimitedHint")}</p>
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.resetStrategy")}
            </label>
            <Select {...register("data_limit_reset_strategy")}>
              {RESET_STRATEGIES.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {t(opt.labelKey)}
                </option>
              ))}
            </Select>
          </div>

          <div>
            <label className="mb-1 block text-xs font-medium text-rapido-muted">
              {t("rapido.expireDaysFromNow")}
            </label>
            <Input type="number" min={0} step="1" {...register("expire_days")} />
            <p className="mt-1 text-xs text-rapido-muted">{t("rapido.emptyNeverHint")}</p>
          </div>
        </div>

        <InboundsPicker
          protocols={visibleProtocols}
          isProtocolUnavailable={(protocol) => !availableProtocols.includes(protocol)}
          inboundsByProtocol={inbounds ?? {}}
          selectedProtocols={selectedProtocols}
          onToggleProtocol={toggleProtocol}
          selectedInbounds={selectedInbounds}
          onToggleTag={toggleInboundTag}
        />

        <div>
          <label className="mb-1 block text-xs font-medium text-rapido-muted">
            {t("userDialog.note")}
          </label>
          <textarea
            rows={3}
            {...register("note")}
            className="w-full resize-none rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 text-sm text-rapido-text placeholder:text-rapido-muted focus:outline-none focus:ring-1 focus:ring-rapido-accent"
          />
        </div>

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="secondary" onClick={close} disabled={submitting}>
            {t("cancel")}
          </Button>
          <Button variant="primary" type="submit" disabled={submitting || !canSubmit}>
            {submitting
              ? t("rapido.saving")
              : isEdit
              ? t("rapido.saveChanges")
              : t("createUser")}
          </Button>
        </div>
      </form>
    </Modal>
  );
};

export default UserFormModal;
