import { FC, ReactNode, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { QRCodeSVG } from "qrcode.react";
import CopyToClipboard from "react-copy-to-clipboard";
import {
  useDeleteUserMutation,
  useResetUserUsageMutation,
  useRevokeUserSubMutation,
} from "hooks/useUsersQuery";
import { useUsersUiStore } from "rapido-ui/usersUiStore";
import { User } from "types/User";
import { errorText } from "service/errors";
import { Modal } from "rapido-ui/Modal";
import { Button } from "rapido-ui/Button";

// The *.prompt locale entries embed literal <b>{{username}}</b>, so they are
// rendered through <Trans> - interpolating them into a string would print the
// tags, and dangerouslySetInnerHTML on translator-supplied text is not worth it.
const PromptText: FC<{ i18nKey: string; username: string }> = ({
  i18nKey,
  username,
}) => (
  <Trans
    i18nKey={i18nKey}
    values={{ username }}
    // dir="ltr" isolates the username: it is an ASCII token dropped into a
    // Persian sentence, and without it the punctuation around it changes sides.
    components={{ b: <b dir="ltr" className="font-medium text-rapido-text" /> }}
  />
);

type ConfirmModalProps = {
  title: string;
  description: ReactNode;
  confirmLabel: string;
  confirmDanger?: boolean;
  onCancel: () => void;
  onConfirm: () => Promise<unknown>;
};

const ConfirmActionModal: FC<ConfirmModalProps> = ({
  title,
  description,
  confirmLabel,
  confirmDanger,
  onCancel,
  onConfirm,
}) => {
  const { t } = useTranslation();
  const [isPending, setIsPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleConfirm = () => {
    setError(null);
    setIsPending(true);
    onConfirm()
      .catch((e: unknown) => {
        setError(errorText(e, t("rapido.somethingWentWrong")));
      })
      .finally(() => {
        setIsPending(false);
      });
  };

  return (
    <Modal onClose={onCancel} title={title}>
      <p className="mb-4 text-sm text-rapido-muted">{description}</p>
      {error && (
        <p className="mb-3 text-sm text-red-400" role="alert">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onCancel} disabled={isPending}>
          {t("cancel")}
        </Button>
        <button
          type="button"
          onClick={handleConfirm}
          disabled={isPending}
          className={
            "rounded-lg px-3 py-2 text-sm font-medium text-white transition-colors disabled:cursor-not-allowed disabled:opacity-50 " +
            (confirmDanger
              ? "bg-red-600 hover:bg-red-500"
              : "bg-rapido-accent hover:bg-rapido-accent/90")
          }
        >
          {isPending ? t("rapido.pleaseWait") : confirmLabel}
        </button>
      </div>
    </Modal>
  );
};

const DeleteUserConfirmModal: FC<{ deletingUser: User }> = ({ deletingUser }) => {
  const { t } = useTranslation();
  const setDeletingUser = useUsersUiStore((s) => s.setDeletingUser);
  const deleteUser = useDeleteUserMutation();

  return (
    <ConfirmActionModal
      title={t("deleteUser.title")}
      description={<PromptText i18nKey="deleteUser.prompt" username={deletingUser.username} />}
      confirmLabel={t("delete")}
      confirmDanger
      onCancel={() => setDeletingUser(null)}
      onConfirm={() =>
        deleteUser.mutateAsync(deletingUser.username).then(() => setDeletingUser(null))
      }
    />
  );
};

const ResetUsageConfirmModal: FC<{ resetUsageUser: User }> = ({ resetUsageUser }) => {
  const { t } = useTranslation();
  const setResetUsageUser = useUsersUiStore((s) => s.setResetUsageUser);
  const resetUsage = useResetUserUsageMutation();

  return (
    <ConfirmActionModal
      title={t("resetUserUsage.title")}
      description={<PromptText i18nKey="resetUserUsage.prompt" username={resetUsageUser.username} />}
      confirmLabel={t("reset")}
      onCancel={() => setResetUsageUser(null)}
      onConfirm={() =>
        resetUsage.mutateAsync(resetUsageUser.username).then(() => setResetUsageUser(null))
      }
    />
  );
};

const RevokeSubscriptionConfirmModal: FC<{ revokeSubscriptionUser: User }> = ({
  revokeSubscriptionUser,
}) => {
  const { t } = useTranslation();
  const setRevokeSubscriptionUser = useUsersUiStore((s) => s.setRevokeSubscriptionUser);
  const revokeSub = useRevokeUserSubMutation();

  return (
    <ConfirmActionModal
      title={t("revokeUserSub.title")}
      description={
        <PromptText i18nKey="revokeUserSub.prompt" username={revokeSubscriptionUser.username} />
      }
      confirmLabel={t("revoke")}
      confirmDanger
      onCancel={() => setRevokeSubscriptionUser(null)}
      onConfirm={() =>
        revokeSub
          .mutateAsync(revokeSubscriptionUser.username)
          .then(() => setRevokeSubscriptionUser(null))
      }
    />
  );
};

// The backend returns a bare "/sub/<token>" path whenever
// XRAY_SUBSCRIPTION_URL_PREFIX is unset, which is useless once copied out of
// the panel - so resolve it against the panel's own origin, exactly as the
// old dashboard did.
export const absoluteSubscriptionUrl = (url: string): string =>
  url.startsWith("/") ? window.location.origin + url : url;

// Merges the old dashboard's separate "QR code grid" and "copy subscription
// link" modals into one (see the plan's Users notes): `subscription_url` is
// the only link the Go backend returns today - there is no per-format
// `links[]` array yet - so there is nothing left to show a *grid* of. One QR
// code beside one copyable field covers the same need.
const SubscriptionLinkModal: FC<{ user: User }> = ({ user }) => {
  const { t } = useTranslation();
  const setSubscriptionLinkUser = useUsersUiStore((s) => s.setSubscriptionLinkUser);
  const [copied, setCopied] = useState(false);
  const close = () => setSubscriptionLinkUser(null);
  const fullUrl = absoluteSubscriptionUrl(user.subscription_url);

  return (
    <Modal onClose={close} title={t("rapido.subscriptionLink")}>
      <div className="mb-3 flex justify-center rounded-lg bg-white p-3">
        <QRCodeSVG value={fullUrl} size={180} />
      </div>
      <div className="flex gap-2">
        {/* A URL read right-to-left is unusable: the scheme lands at the far
            end and the slashes migrate. dir="ltr" pins the field's own
            direction regardless of the page's. */}
        <input
          readOnly
          dir="ltr"
          value={fullUrl}
          onFocus={(e) => e.currentTarget.select()}
          className="min-w-0 flex-1 rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 text-sm text-rapido-text"
        />
        <CopyToClipboard
          text={fullUrl}
          onCopy={() => {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1500);
          }}
        >
          <Button variant="primary" className="shrink-0">
            {copied ? t("usersTable.copied") : t("rapido.copy")}
          </Button>
        </CopyToClipboard>
      </div>
      <div className="mt-4 flex justify-end">
        <Button variant="secondary" onClick={close}>
          {t("rapido.close")}
        </Button>
      </div>
    </Modal>
  );
};

export const UserActionModals: FC = () => {
  const deletingUser = useUsersUiStore((s) => s.deletingUser);
  const resetUsageUser = useUsersUiStore((s) => s.resetUsageUser);
  const revokeSubscriptionUser = useUsersUiStore((s) => s.revokeSubscriptionUser);
  const subscriptionLinkUser = useUsersUiStore((s) => s.subscriptionLinkUser);

  return (
    <>
      {deletingUser && <DeleteUserConfirmModal deletingUser={deletingUser} />}
      {resetUsageUser && <ResetUsageConfirmModal resetUsageUser={resetUsageUser} />}
      {revokeSubscriptionUser && (
        <RevokeSubscriptionConfirmModal revokeSubscriptionUser={revokeSubscriptionUser} />
      )}
      {subscriptionLinkUser && <SubscriptionLinkModal user={subscriptionLinkUser} />}
    </>
  );
};

export default UserActionModals;
