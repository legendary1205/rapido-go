import { FC, PropsWithChildren, ReactNode } from "react";
import classNames from "classnames";
import { Card, CardTitle } from "./Card";

// Promotes the Overlay+ModalCard pair that used to live only inside
// UserActionModals.tsx (and was hand-duplicated, slightly differently sized,
// inline in UserFormModal.tsx and AdminsAdmin.tsx's AdminForm). The
// click-outside-to-close check matters: it compares `e.target ===
// e.currentTarget` rather than just listening for any click on the overlay,
// so a mousedown that starts inside the card and is dragged/released outside
// it (selecting text, for instance) does not close the modal.
export type ModalProps = PropsWithChildren<{
  onClose: () => void;
  /** Rendered via CardTitle above `children` when given. Omit it to build a
   * fully custom header (a title plus a subtitle plus a close button, say)
   * as part of `children` instead. */
  title?: ReactNode;
  /** Overrides the card's default `max-w-sm` sizing, e.g. "max-w-2xl". */
  className?: string;
}>;

export const Modal: FC<ModalProps> = ({ onClose, title, className, children }) => (
  <div
    className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
    onMouseDown={(e) => {
      if (e.target === e.currentTarget) onClose();
    }}
  >
    <Card className={classNames("max-h-[90vh] w-full max-w-sm overflow-y-auto", className)}>
      {title !== undefined && <CardTitle className="mb-3 text-base">{title}</CardTitle>}
      {children}
    </Card>
  </div>
);
