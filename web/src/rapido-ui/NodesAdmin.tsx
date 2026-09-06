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
import { Node, NodeCreateResult, NodeWritePayload } from "types/Node";
import { errorText } from "service/errors";
import { formatBytes } from "utils/formatByte";
import { toneForNodeStatus } from "utils/nodeStatus";
import { Card } from "rapido-ui/Card";
import { Badge, BadgeTone } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";
import { Checkbox } from "rapido-ui/Checkbox";
import { Modal } from "rapido-ui/Modal";

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
  disabled: boolean;
};

const defaultFormValues = (): NodeFormValues => ({
  name: "",
  address: "",
  port: "62050",
  api_port: "62051",
  usage_coefficient: "1",
  disabled: false,
});

const formValuesFromNode = (n: Node): NodeFormValues => ({
  name: n.name,
  address: n.address,
  port: String(n.port),
  api_port: String(n.api_port),
  usage_coefficient: String(n.usage_coefficient),
  disabled: n.status === "disabled",
});

const NodeFormModal: FC<{
  initial: Node | null;
  onClose: () => void;
  onCreated: (result: NodeCreateResult) => void;
}> = ({ initial, onClose, onCreated }) => {
  const { t } = useTranslation();
  const isEdit = !!initial;
  const [values, setValues] = useState<NodeFormValues>(
    initial ? formValuesFromNode(initial) : defaultFormValues()
  );
  const [error, setError] = useState("");

  const createNode = useCreateNodeMutation();
  const updateNode = useUpdateNodeMutation();
  const saving = createNode.isPending || updateNode.isPending;

  const set = (patch: Partial<NodeFormValues>) => setValues((v) => ({ ...v, ...patch }));

  const submit = () => {
    setError("");
    const body: NodeWritePayload = {
      name: values.name,
      address: values.address,
      port: Number(values.port),
      api_port: Number(values.api_port),
      usage_coefficient:
        values.usage_coefficient === "" ? undefined : Number(values.usage_coefficient),
    };

    if (isEdit) {
      updateNode.mutate(
        { id: initial!.id, body: { ...body, disabled: values.disabled } },
        {
          onSuccess: onClose,
          onError: (e) => setError(errorText(e, t("rapido.nodes.saveFailed"))),
        }
      );
    } else {
      createNode.mutate(body, {
        onSuccess: onCreated,
        onError: (e) => setError(errorText(e, t("rapido.nodes.saveFailed"))),
      });
    }
  };

  const canSubmit =
    !!values.name.trim() && !!values.address.trim() && !!values.port && !!values.api_port;

  return (
    <Modal onClose={onClose} className="max-w-md">
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

        {error && (
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
        <CopyField label={t("rapido.nodes.reportSecret")} value={result.report_secret} />
        <CopyField label={t("nodes.certificate")} value={result.certificate} />
        <CopyField label={t("rapido.nodes.privateKey")} value={result.key} />
        <CopyField label={t("rapido.nodes.caCertificate")} value={result.ca_certificate} />

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

const NodeCard: FC<{ node: Node; onEdit: () => void }> = ({ node, onEdit }) => {
  const { t } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const deleteNode = useDeleteNodeMutation();
  const updateNode = useUpdateNodeMutation();

  const tone = toneForNodeStatus(node.status);
  const isDisabled = node.status === "disabled";

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
          </div>
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
            <NodeCard key={node.id} node={node} onEdit={() => setEditing(node)} />
          ))}
        </div>
      )}

      {editing !== undefined && (
        <NodeFormModal
          initial={editing}
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
