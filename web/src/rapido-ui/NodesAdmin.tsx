import { FC, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import {
  useCreateNodeMutation,
  useDeleteNodeMutation,
  useNodesQuery,
  useNodesUsageQuery,
  useUpdateNodeMutation,
} from "hooks/useNodesQuery";
import { useInboundsQuery } from "hooks/useInboundsQuery";
import { useHostsLoadQuery } from "hooks/useHostsLoadQuery";
import { NodeLoadEntry } from "types/HostLoad";
import { Node, NodeCreateResult, NodeWritePayload } from "types/Node";
import { errorText } from "service/errors";
import { formatBytes } from "utils/formatByte";
import { toneForNodeStatus } from "utils/nodeStatus";
import { buildNodeInstallCommand } from "utils/nodeInstall";
import { PortInputError } from "utils/inboundPorts";
import {
  CapacityInputError,
  MAX_NODE_CAPACITY,
  MIN_NODE_CAPACITY,
  formatCapacityInput,
  parseCapacityInput,
} from "utils/nodeCapacity";
import { nodeLoadById } from "utils/hostLoad";
import {
  NodeProfileDraft,
  OverridesInputError,
  buildNodeProfile,
  draftFromNode,
  emptyNodeProfileDraft,
  hasCustomProfile,
  profileFieldOfServerError,
  profileForCreate,
  summarizeNodeProfile,
  tagChoices,
  toggleTag,
} from "utils/nodeProfile";
import { ltrIsolate } from "rapido-ui/bidi";
import { Card } from "rapido-ui/Card";
import { Badge, BadgeTone } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";
import { Checkbox } from "rapido-ui/Checkbox";
import { Modal } from "rapido-ui/Modal";
import { NodeLoadChip } from "rapido-ui/NodeLoadChip";

// Full CRUD for the physical/VPS servers a node agent runs on - not to be
// confused with rapido-ui/HostsAdmin.tsx's Hosts, which are proxy connection
// endpoints handed out in subscriptions. Follows the same list-of-cards +
// modal shape as AdminsAdmin.tsx/HostsAdmin.tsx/UserTemplatesAdmin.tsx.
//
// Two things this page deliberately does NOT carry over from the old
// (Python-backed) dashboard's equivalent:
//  - No "reconnect" button and no shared certificate panel: this backend has
//    no /node/:id/reconnect or /node/settings endpoints at all. Instead,
//    every node gets its own leaf certificate issued once at creation time
//    (see NodeCreatedPanel below) - a real per-node identity instead of one
//    shared cert every node TOFU'd on connect.
//  - No "add this node as a host for every inbound" checkbox on create:
//    nodeCreateRequest (internal/httpapi/node.go) has no such field: hosts
//    are managed entirely separately on the Hosts page in this backend.

const cardToneClasses: Partial<Record<BadgeTone, string>> = {
  green: "!border-emerald-500/60 bg-emerald-500/[0.04]",
  yellow: "!border-yellow-500/60 bg-yellow-500/[0.04]",
  red: "!border-red-500/60 bg-red-500/[0.04]",
};

// ---------------------------------------------------------------------------

type NodeFormValues = {
  name: string;
  address: string;
  port: string;
  api_port: string;
  usage_coefficient: string;
  // Client connections at 100% load, as typed; empty is the panel default.
  capacity: string;
  disabled: boolean;
  // Create-only - see NodeWritePayload's own doc comment. Never populated
  // from an existing node (formValuesFromNode has no source for it, the
  // panel never stores it), so it's simply absent from the edit form.
  panel_url: string;
  // Which inbounds/ports the node serves and what it overrides - see
  // utils/nodeProfile.ts. Empty everywhere is the default profile.
  profile: NodeProfileDraft;
};

const defaultFormValues = (): NodeFormValues => ({
  name: "",
  address: "",
  port: "62050",
  api_port: "62051",
  usage_coefficient: "1",
  capacity: "",
  disabled: false,
  panel_url: "",
  profile: emptyNodeProfileDraft(),
});

const formValuesFromNode = (n: Node): NodeFormValues => ({
  name: n.name,
  address: n.address,
  port: String(n.port),
  api_port: String(n.api_port),
  usage_coefficient: String(n.usage_coefficient),
  capacity: formatCapacityInput(n.capacity),
  disabled: n.status === "disabled",
  panel_url: "",
  profile: draftFromNode(n),
});

const cap = (s: string) => s[0].toUpperCase() + s.slice(1);

// The server's own validation messages for these fields are English and
// technical, so they are shown as they come, left-to-right, under the field.
const ServerFieldError: FC<{ message: string }> = ({ message }) => (
  <span
    role="alert"
    dir="ltr"
    className="whitespace-pre-wrap break-words rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400"
  >
    {message}
  </span>
);

const NodeFormModal: FC<{
  initial: Node | null;
  /** The panel-wide capacity a node without its own uses, when known: shown as
   * the capacity field's placeholder. */
  defaultCapacity?: number;
  onClose: () => void;
  onCreated: (result: NodeCreateResult) => void;
}> = ({ initial, defaultCapacity, onClose, onCreated }) => {
  const { t } = useTranslation();
  const isEdit = !!initial;
  const [values, setValues] = useState<NodeFormValues>(
    initial ? formValuesFromNode(initial) : defaultFormValues()
  );
  const [error, setError] = useState("");
  // Also open for a node that has its own capacity: it is set on purpose and
  // easy to lose track of when it is folded away.
  const [advancedOpen, setAdvancedOpen] = useState(
    !!initial && (hasCustomProfile(initial) || initial.capacity != null)
  );

  const createNode = useCreateNodeMutation();
  const updateNode = useUpdateNodeMutation();
  const saving = createNode.isPending || updateNode.isPending;
  const inbounds = useInboundsQuery();
  const knownTags = useMemo(
    () => Object.values(inbounds.data ?? {}).flat().sort((a, b) => a.localeCompare(b)),
    [inbounds.data]
  );

  const set = (patch: Partial<NodeFormValues>) => setValues((v) => ({ ...v, ...patch }));
  const setProfile = (patch: Partial<NodeProfileDraft>) =>
    setValues((v) => ({ ...v, profile: { ...v.profile, ...patch } }));

  const profileResult = useMemo(() => buildNodeProfile(values.profile), [values.profile]);
  const portsError = profileResult.ok ? null : profileResult.ports ?? null;
  const overridesError = profileResult.ok ? null : profileResult.overrides ?? null;
  const capacityResult = useMemo(() => parseCapacityInput(values.capacity), [values.capacity]);
  const capacityError = capacityResult.ok ? null : capacityResult.error;
  // The panel starts each profile validation message with the field's name,
  // so it can be shown under that field instead of only at the bottom.
  const serverField = error ? profileFieldOfServerError(error) : null;

  const portsErrorText = (e: PortInputError) =>
    t(`rapido.xrayConfig.portError${cap(e.kind)}`, { value: ltrIsolate(e.value) });
  const capacityErrorText = (e: CapacityInputError) =>
    t(`rapido.nodes.capacityError.${e.kind}`, {
      value: ltrIsolate(e.value),
      min: ltrIsolate(MIN_NODE_CAPACITY),
      max: ltrIsolate(MAX_NODE_CAPACITY.toLocaleString("en-US")),
    });
  const overridesErrorText = (e: OverridesInputError): string => {
    switch (e.kind) {
      case "invalidJson":
        return t("rapido.nodes.overrides.invalidJson", { message: ltrIsolate(e.message) });
      case "notObject":
        return t("rapido.nodes.overrides.notObject", { example: ltrIsolate('{"log_level": "debug"}') });
      case "unknownKey":
        return t("rapido.nodes.overrides.unknownKey", { key: ltrIsolate(e.key) });
      case "notString":
      case "notBoolean":
      case "notList":
        return t(`rapido.nodes.overrides.${e.kind}`, { key: ltrIsolate(e.key) });
      case "invalidLogLevel":
        return t("rapido.nodes.overrides.invalidLogLevel", { value: ltrIsolate(e.value) });
    }
  };

  const onSaveError = (e: unknown) => {
    const message = errorText(e, t("rapido.nodes.saveFailed"));
    setError(message);
    if (profileFieldOfServerError(message)) setAdvancedOpen(true);
  };

  const submit = () => {
    setError("");
    if (!profileResult.ok || !capacityResult.ok) {
      setAdvancedOpen(true);
      return;
    }
    const profile = profileResult.profile;
    const capacity = capacityResult.capacity;
    const body: NodeWritePayload = {
      name: values.name,
      address: values.address,
      port: Number(values.port),
      api_port: Number(values.api_port),
      usage_coefficient:
        values.usage_coefficient === "" ? undefined : Number(values.usage_coefficient),
      // Update has no panel_url field at all (see NodeWritePayload's own
      // comment) - only send it on create, and only when non-empty, so an
      // update body never carries a stray key the backend would ignore
      // anyway but that has no business being there.
      ...(!isEdit && values.panel_url.trim() ? { panel_url: values.panel_url.trim() } : {}),
      // Create sends only what is customised; an update sends all three
      // explicitly, because an omitted key would leave the stored value alone
      // and an emptied field has to clear it.
      ...(isEdit ? profile : profileForCreate(profile)),
      // Same rule for the capacity: a new node with the panel default carries
      // no key at all, while an update always says which it means - an
      // explicit null is what clears one the node currently has.
      ...(isEdit || capacity !== null ? { capacity } : {}),
    };

    if (isEdit) {
      updateNode.mutate(
        { id: initial!.id, body: { ...body, disabled: values.disabled } },
        { onSuccess: onClose, onError: onSaveError }
      );
    } else {
      createNode.mutate(body, { onSuccess: onCreated, onError: onSaveError });
    }
  };

  const canSubmit =
    !!values.name.trim() &&
    !!values.address.trim() &&
    !!values.port &&
    !!values.api_port &&
    profileResult.ok &&
    capacityResult.ok;
  const choices = tagChoices(knownTags, values.profile.tags);

  return (
    <Modal onClose={onClose} className="max-w-lg">
      <h2 className="mb-4 text-lg font-semibold">
        {isEdit ? t("nodes.editNode") : t("nodes.addNode")}
      </h2>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("nodes.nodeName")}</span>
          <Input value={values.name} onChange={(e) => set({ name: e.target.value })} />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("nodes.nodeAddress")}</span>
          <Input dir="ltr" value={values.address} onChange={(e) => set({ address: e.target.value })} />
        </label>
        {/* Create-only: baked into the one-paste setup blob below, never
            stored on the node row itself - see NodeWritePayload's comment
            on why the edit form has no equivalent field. */}
        {!isEdit && (
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.nodes.panelUrlField")}</span>
            <Input
              dir="ltr"
              placeholder="https://panel.example.com:8001"
              value={values.panel_url}
              onChange={(e) => set({ panel_url: e.target.value })}
            />
            <span className="text-xs text-rapido-muted">{t("rapido.nodes.panelUrlHint")}</span>
          </label>
        )}
        <div className="grid grid-cols-2 gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("nodes.nodePort")}</span>
            <Input
              dir="ltr"
              type="number"
              value={values.port}
              onChange={(e) => set({ port: e.target.value })}
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("nodes.nodeAPIPort")}</span>
            <Input
              dir="ltr"
              type="number"
              value={values.api_port}
              onChange={(e) => set({ api_port: e.target.value })}
            />
          </label>
        </div>
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("nodes.usageCoefficient")}</span>
          <Input
            dir="ltr"
            type="number"
            step="0.1"
            value={values.usage_coefficient}
            onChange={(e) => set({ usage_coefficient: e.target.value })}
          />
          {/* Non-obvious and easy to get expensively wrong: it multiplies the
              traffic this node bills to the customer. */}
          <span className="text-xs text-rapido-muted">
            {t("rapido.nodes.usageCoefficientHint")}
          </span>
        </label>

        {/* disabled only exists on PUT (nodeUpdateRequest) - there is no such
            field on create, a brand-new node always starts enabled. */}
        {isEdit && (
          <Checkbox
            checked={values.disabled}
            onChange={(e) => set({ disabled: e.target.checked })}
            label={t("rapido.nodes.disableField")}
          />
        )}

        {/* Closed unless the node already has a custom profile (or the panel
            just rejected one): the common case is a plain node and should not
            have to look at any of this. */}
        <details
          open={advancedOpen}
          onToggle={(e) => setAdvancedOpen(e.currentTarget.open)}
          className="rounded-lg border border-rapido-border"
        >
          <summary className="cursor-pointer select-none px-3 py-2 text-sm text-rapido-muted">
            {t("rapido.nodes.advanced")}
          </summary>
          <div className="flex flex-col gap-4 border-t border-rapido-border px-3 py-3">
            <p className="text-xs text-rapido-muted">{t("rapido.nodes.advancedHint")}</p>

            {/* Not part of the profile: it changes nothing the node serves, only
                what this node's load chip and the configs on it count as full. */}
            <label className="flex flex-col gap-1">
              <span className="text-xs font-medium text-rapido-text">{t("rapido.nodes.capacityField")}</span>
              <Input
                dir="ltr"
                inputMode="numeric"
                placeholder={defaultCapacity ? String(defaultCapacity) : undefined}
                hasError={!!capacityError || serverField === "capacity"}
                aria-invalid={!!capacityError}
                value={values.capacity}
                onChange={(e) => set({ capacity: e.target.value })}
              />
              {capacityError ? (
                <span role="alert" className="text-xs text-red-400">
                  {capacityErrorText(capacityError)}
                </span>
              ) : (
                <span className="text-xs text-rapido-muted">{t("rapido.nodes.capacityHint")}</span>
              )}
              {serverField === "capacity" && <ServerFieldError message={error} />}
            </label>

            <div className="flex flex-col gap-1.5">
              <span className="text-xs font-medium text-rapido-text">{t("rapido.nodes.servesInbounds")}</span>
              {choices.length > 0 ? (
                <div className="flex flex-wrap gap-x-4 gap-y-1.5">
                  {choices.map((tag) => (
                    <Checkbox
                      key={tag}
                      checked={values.profile.tags.includes(tag)}
                      onChange={() => setProfile({ tags: toggleTag(values.profile.tags, tag) })}
                      label={
                        <>
                          <span dir="ltr">{tag}</span>
                          {!knownTags.includes(tag) && (
                            <span className="ms-1 text-xs text-rapido-muted">
                              ({t("rapido.nodes.inboundMissing")})
                            </span>
                          )}
                        </>
                      }
                    />
                  ))}
                </div>
              ) : (
                <p className="text-xs text-rapido-muted">{t("rapido.nodes.noInbounds")}</p>
              )}
              <span className="text-xs text-rapido-muted">{t("rapido.nodes.servesInboundsHint")}</span>
              {serverField === "inbound_tags" && <ServerFieldError message={error} />}
            </div>

            <label className="flex flex-col gap-1">
              <span className="text-xs font-medium text-rapido-text">{t("rapido.nodes.listenPorts")}</span>
              <Input
                dir="ltr"
                inputMode="numeric"
                placeholder="20000, 20001"
                hasError={!!portsError || serverField === "listen_ports"}
                aria-invalid={!!portsError}
                value={values.profile.portsText}
                onChange={(e) => setProfile({ portsText: e.target.value })}
              />
              {portsError ? (
                <span role="alert" className="text-xs text-red-400">
                  {portsErrorText(portsError)}
                </span>
              ) : (
                <span className="text-xs text-rapido-muted">
                  {t("rapido.nodes.listenPortsHint", { example: ltrIsolate("20000, 20001") })}
                </span>
              )}
              {serverField === "listen_ports" && <ServerFieldError message={error} />}
            </label>

            <label className="flex flex-col gap-1">
              <span className="text-xs font-medium text-rapido-text">{t("rapido.nodes.coreOverrides")}</span>
              <textarea
                dir="ltr"
                spellCheck={false}
                rows={8}
                placeholder={'{\n  "log_level": "debug"\n}'}
                aria-invalid={!!overridesError}
                className={classNames(
                  "w-full rounded-lg border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text placeholder:text-rapido-muted focus:outline-none focus:ring-1 focus:ring-rapido-accent",
                  overridesError || serverField === "core_overrides" ? "border-red-500" : "border-rapido-border"
                )}
                value={values.profile.overridesText}
                onChange={(e) => setProfile({ overridesText: e.target.value })}
              />
              {overridesError ? (
                <span role="alert" className="text-xs text-red-400">
                  {overridesErrorText(overridesError)}
                </span>
              ) : (
                <span className="text-xs text-rapido-muted">{t("rapido.nodes.coreOverridesHint")}</span>
              )}
              {serverField === "core_overrides" && <ServerFieldError message={error} />}
            </label>
          </div>
        </details>

        {error && !serverField && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t("cancel")}
          </Button>
          <Button variant="primary" disabled={saving || !canSubmit} onClick={submit}>
            {saving ? t("rapido.pleaseWait") : isEdit ? t("nodes.editNode") : t("nodes.addNode")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

// ---------------------------------------------------------------------------

const CopyField: FC<{ label: string; value: string }> = ({ label, value }) => {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  const copy = () => {
    navigator.clipboard?.writeText(value).then(
      () => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 2000);
      },
      () => undefined
    );
  };

  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs font-medium text-rapido-text">{label}</span>
      <textarea
        readOnly
        dir="ltr"
        spellCheck={false}
        rows={3}
        aria-label={label}
        className="w-full resize-none rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
        value={value}
        onFocus={(e) => e.currentTarget.select()}
      />
      <Button variant="chip" tone={copied ? "accent" : "neutral"} className="self-start" onClick={copy}>
        {copied ? t("usersTable.copied") : t("rapido.copy")}
      </Button>
    </div>
  );
};

// Modeled on the "download your API key, we'll never show it again" pattern:
// a dedicated panel the admin must explicitly dismiss, not a toast that
// vanishes on its own. onClose is deliberately never wired to Modal's
// click-outside handler (see the no-op below) so a stray click outside the
// card can't discard secrets before they're copied.
const NodeCreatedPanel: FC<{ result: NodeCreateResult; onClose: () => void }> = ({
  result,
  onClose,
}) => {
  const { t } = useTranslation();
  return (
    <Modal onClose={() => undefined} className="max-w-lg">
      <h2 className="mb-1 text-lg font-semibold">
        {t("rapido.nodes.createdTitle", { name: result.node.name })}
      </h2>
      <div className="mb-4 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
        {t("rapido.nodes.oneTimeWarning")}
      </div>
      <div className="flex flex-col gap-4">
        {/* The way most admins will actually bring the node up, so it comes
            first and looks like it: one command, built from where the panel is
            reached from right now plus the one-time blob. */}
        <div className="flex flex-col gap-2 rounded-lg border border-rapido-accent/50 bg-rapido-accent/[0.06] p-3">
          <div className="text-sm font-semibold text-rapido-text">{t("rapido.nodes.installTitle")}</div>
          <p className="text-xs text-rapido-muted">{t("rapido.nodes.installHelp")}</p>
          <CopyField
            label={t("rapido.nodes.installCommand")}
            value={buildNodeInstallCommand(window.location.origin, result.setup_blob)}
          />
        </div>

        <CopyField label={t("rapido.nodes.setupBlob")} value={result.setup_blob} />
        <p className="text-xs text-rapido-muted">{t("rapido.nodes.setupBlobHint")}</p>

        {/* Native <details> - no extra state needed, and it's closed by
            default so the one blob above stays the thing an admin actually
            copies in the common case. Kept for a manual/scripted setup or
            for inspecting what the blob actually contains. */}
        <details className="rounded-lg border border-rapido-border">
          <summary className="cursor-pointer select-none px-3 py-2 text-sm text-rapido-muted">
            {t("rapido.nodes.showRawValues")}
          </summary>
          <div className="flex flex-col gap-4 border-t border-rapido-border px-3 py-3">
            <CopyField label={t("rapido.nodes.reportSecret")} value={result.report_secret} />
            <CopyField label={t("nodes.certificate")} value={result.certificate} />
            <CopyField label={t("rapido.nodes.privateKey")} value={result.key} />
            <CopyField label={t("rapido.nodes.caCertificate")} value={result.ca_certificate} />
          </div>
        </details>

        <div className="flex justify-end">
          <Button variant="primary" onClick={onClose}>
            {t("rapido.close")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

// ---------------------------------------------------------------------------

const NodeCard: FC<{
  node: Node;
  /** Live load of this node, when the panel reports one. */
  load?: NodeLoadEntry;
  /** The panel-wide capacity a node without its own uses, when known. */
  defaultCapacity?: number;
  onEdit: () => void;
}> = ({ node, load, defaultCapacity, onEdit }) => {
  const { t } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const deleteNode = useDeleteNodeMutation();
  const updateNode = useUpdateNodeMutation();

  const tone = toneForNodeStatus(node.status);
  const isDisabled = node.status === "disabled";

  const custom = hasCustomProfile(node);
  const profile = summarizeNodeProfile(node);
  // Hover text for the badge: what exactly is different about this node.
  const profileTitle = t("rapido.nodes.profileTitle", {
    inbounds: profile.tags.length ? profile.tags.join(", ") : t("rapido.nodes.profileAll"),
    ports: profile.ports.length ? profile.ports.join(", ") : t("rapido.nodes.profileAll"),
    overrides: profile.overrideKeys.length ? profile.overrideKeys.join(", ") : t("rapido.nodes.profileNone"),
  });

  const remove = () => {
    setMsg(null);
    deleteNode.mutate(node.id, {
      onError: (e) => {
        setMsg({ tone: "err", text: errorText(e, t("rapido.nodes.deleteFailed")) });
        setConfirmDelete(false);
      },
    });
  };

  // There is no separate enable/disable endpoint - PUT /api/node/:id is a
  // full-field update, so flipping just `disabled` still has to resend the
  // node's other current values (see nodeUpdateRequest's own doc comment).
  const toggleDisabled = () => {
    setMsg(null);
    updateNode.mutate(
      {
        id: node.id,
        body: {
          name: node.name,
          address: node.address,
          port: node.port,
          api_port: node.api_port,
          usage_coefficient: node.usage_coefficient,
          disabled: !isDisabled,
        },
      },
      { onError: (e) => setMsg({ tone: "err", text: errorText(e, t("rapido.nodes.saveFailed")) }) }
    );
  };

  return (
    <Card className={classNames("p-4", cardToneClasses[tone])}>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate text-sm font-semibold">{node.name}</span>
            <Badge tone={tone}>{t(`nodes.status.${node.status}`, node.status)}</Badge>
            {custom && (
              <Badge tone="brand" title={profileTitle}>
                {t("rapido.nodes.customProfile")}
              </Badge>
            )}
          </div>
          {load && <NodeLoadChip load={load} />}
        </div>

        <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-rapido-muted">
          <span>
            {t("nodes.nodeAddress")}:{" "}
            <span className="text-rapido-text" dir="ltr">
              {node.address}
            </span>
          </span>
          <span>
            {t("nodes.nodePort")}:{" "}
            <span className="text-rapido-text" dir="ltr">
              {node.port}
            </span>
          </span>
          <span>
            {t("nodes.nodeAPIPort")}:{" "}
            <span className="text-rapido-text" dir="ltr">
              {node.api_port}
            </span>
          </span>
          <span title={t("rapido.nodes.capacityField")}>
            {t("rapido.nodes.capacity")}:{" "}
            {node.capacity != null ? (
              <span className="text-rapido-text" dir="ltr">
                {node.capacity}
              </span>
            ) : (
              <span className="text-rapido-text">
                {defaultCapacity
                  ? t("rapido.nodes.capacityDefaultValue", { value: ltrIsolate(defaultCapacity) })
                  : t("rapido.nodes.capacityDefault")}
              </span>
            )}
          </span>
          {node.usage_coefficient !== 1 && (
            <span>
              {t("nodes.usageCoefficient")}:{" "}
              <span className="text-rapido-text" dir="ltr">
                ×{node.usage_coefficient}
              </span>
            </span>
          )}
        </div>

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
          <Button
            variant="chip"
            tone={isDisabled ? "sky" : "amber"}
            disabled={updateNode.isPending}
            onClick={toggleDisabled}
          >
            {isDisabled ? t("rapido.hosts.enable") : t("rapido.hosts.disable")}
          </Button>
          {confirmDelete ? (
            <>
              <span className="text-xs text-red-400">{t("rapido.nodes.confirmDelete")}</span>
              <Button variant="chip" tone="red" disabled={deleteNode.isPending} onClick={remove}>
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
    </Card>
  );
};

// ---------------------------------------------------------------------------

const PERIODS = [
  { key: "24h", days: 1 },
  { key: "7d", days: 7 },
  { key: "30d", days: 30 },
];

const UsagePanel: FC = () => {
  const { t } = useTranslation();
  const [days, setDays] = useState(7);
  // Recomputed only when `days` changes, not on every render - a fresh
  // Date.now() each render would change the query key and refetch forever.
  const start = useMemo(() => new Date(Date.now() - days * 86400000).toISOString(), [days]);
  const { data, isLoading, isError } = useNodesUsageQuery(start);
  const rows = data?.usages ?? [];

  const totals = useMemo(() => {
    let max = 0;
    let sum = 0;
    rows.forEach((r) => {
      const total = (r.uplink || 0) + (r.downlink || 0);
      if (total > max) max = total;
      sum += total;
    });
    return { max, sum };
  }, [rows]);

  return (
    <Card className="p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-sm font-semibold">{t("rapido.nodes.usageTitle")}</div>
          <div className="text-xs text-rapido-muted">
            {t("rapido.nodes.usageTotal")}{" "}
            <span className="tabular-nums text-rapido-text" dir="ltr">
              {formatBytes(totals.sum)}
            </span>
          </div>
        </div>
        <div className="flex gap-1">
          {PERIODS.map((p) => (
            <Button
              key={p.key}
              variant="chip"
              tone={days === p.days ? "accent" : "neutral"}
              aria-pressed={days === p.days}
              onClick={() => setDays(p.days)}
            >
              {t(`rapido.nodes.period.${p.key}`)}
            </Button>
          ))}
        </div>
      </div>

      {isError ? (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          {t("rapido.nodes.usageFailed")}
        </div>
      ) : isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-rapido-muted">{t("rapido.nodes.usageEmpty")}</p>
      ) : (
        <div className="flex flex-col gap-2">
          {rows
            .slice()
            .sort((a, b) => b.uplink + b.downlink - (a.uplink + a.downlink))
            .map((r) => {
              const total = (r.uplink || 0) + (r.downlink || 0);
              // Percentages are of the busiest node, not of the sum: with one
              // node carrying most of the traffic every other bar would be a
              // sliver and the comparison the panel exists for would be lost.
              const pct = totals.max ? (total / totals.max) * 100 : 0;
              const upPct = total ? ((r.uplink || 0) / total) * 100 : 0;
              return (
                <div key={r.node_id}>
                  <div className="mb-1 flex items-center justify-between gap-2 text-xs">
                    <span className="truncate" dir="ltr">
                      {r.node_name}
                    </span>
                    <span className="tabular-nums shrink-0 text-rapido-muted" dir="ltr">
                      {formatBytes(total)}
                    </span>
                  </div>
                  <div className="h-2 w-full overflow-hidden rounded-full bg-white/5">
                    {/* One bar, split by direction: down is what customers
                        pulled, up is what they sent. */}
                    <div className="flex h-full" style={{ width: `${pct}%` }}>
                      <div className="h-full bg-sky-400/70" style={{ width: `${upPct}%` }} />
                      <div className="h-full flex-1 bg-rapido-accent" />
                    </div>
                  </div>
                  <div className="mt-0.5 flex gap-3 text-[11px] text-rapido-muted">
                    <span>
                      ↑{" "}
                      <span className="tabular-nums" dir="ltr">
                        {formatBytes(r.uplink || 0)}
                      </span>
                    </span>
                    <span>
                      ↓{" "}
                      <span className="tabular-nums" dir="ltr">
                        {formatBytes(r.downlink || 0)}
                      </span>
                    </span>
                  </div>
                </div>
              );
            })}
        </div>
      )}
    </Card>
  );
};

// ---------------------------------------------------------------------------

export const NodesAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: nodes, isLoading, isError } = useNodesQuery();
  // Decoration only, like the pills on the Hosts page: a slow, missing or
  // failing load request never delays the list, and while it is failing the
  // chips are hidden rather than left showing numbers that have stopped moving.
  const { data: load, isError: loadFailed } = useHostsLoadQuery();
  const loadByNodeId = useMemo(() => nodeLoadById(loadFailed ? null : load), [load, loadFailed]);
  // The default only means "per node" on a panel that reports nodes at all;
  // an older one's capacity is per config.
  const defaultCapacity = !loadFailed && load?.nodes && load.capacity > 0 ? load.capacity : undefined;
  const [editing, setEditing] = useState<Node | null | undefined>(undefined);
  const [created, setCreated] = useState<NodeCreateResult | null>(null);

  const rows = nodes ?? [];
  const connected = rows.filter((n) => n.status === "connected").length;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">
          {t("rapido.nodes.summary", { connected, total: rows.length })}
        </div>
        <Button variant="chip" tone="accent" onClick={() => setEditing(null)}>
          + {t("nodes.addNewRapidoNode")}
        </Button>
      </div>

      <UsagePanel />

      {isError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {t("rapido.nodes.loadFailed")}
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : rows.length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">{t("rapido.nodes.empty")}</Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {rows.map((node) => (
            <NodeCard
              key={node.id}
              node={node}
              load={loadByNodeId.get(node.id)}
              defaultCapacity={defaultCapacity}
              onEdit={() => setEditing(node)}
            />
          ))}
        </div>
      )}

      {editing !== undefined && (
        <NodeFormModal
          initial={editing}
          defaultCapacity={defaultCapacity}
          onClose={() => setEditing(undefined)}
          onCreated={(result) => {
            setEditing(undefined);
            setCreated(result);
          }}
        />
      )}

      {created && <NodeCreatedPanel result={created} onClose={() => setCreated(null)} />}
    </div>
  );
};

export default NodesAdmin;
