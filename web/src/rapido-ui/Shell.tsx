import { FC, PropsWithChildren, ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import {
  HomeIcon,
  UsersIcon,
  GlobeAltIcon,
  ShieldCheckIcon,
  PuzzlePieceIcon,
  DocumentDuplicateIcon,
  TicketIcon,
} from "@heroicons/react/24/outline";
import { ReactComponent as Logo } from "assets/logo.svg";
import { useCurrentAdminQuery } from "hooks/useCurrentAdminQuery";
import { removeAuthToken } from "utils/authStorage";
import { LanguageSwitcher } from "rapido-ui/LanguageSwitcher";
import { dirOf } from "utils/language";
import "rapido-ui/tailwind.css";

const NavLink: FC<{ href: string; active?: boolean; icon: FC<{ className?: string }>; children: ReactNode }> = ({
  href,
  active,
  icon: Icon,
  children,
}) => (
  <Link
    to={href}
    className={classNames(
      "flex items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium transition-colors",
      active
        ? "bg-rapido-accent/15 text-rapido-accent"
        : "text-rapido-muted hover:bg-rapido-raised hover:text-rapido-text"
    )}
  >
    <Icon className="h-5 w-5 shrink-0" />
    <span className="truncate">{children}</span>
  </Link>
);

// This phase's whole page list: Nodes/Monitoring/Settings from the old
// dashboard aren't deferred, they simply don't exist yet - node-side live
// monitoring needs a reporting phase that hasn't landed, and Core Config
// doesn't map onto this architecture the same way (see the plan's own
// context on why). Tickets is no longer in that boat: GET/POST/PUT
// /api/tickets* are now implemented (internal/httpapi/tickets.go).
export type RapidoNavKey =
  | "overview"
  | "users"
  | "tickets"
  | "hosts"
  | "admins"
  | "templates"
  | "integrations";

const NAV_ITEMS: {
  key: RapidoNavKey;
  href: string;
  labelKey: string;
  icon: FC<{ className?: string }>;
  sudoOnly?: boolean;
}[] = [
  { key: "overview", href: "/", labelKey: "rapido.overview", icon: HomeIcon },
  { key: "users", href: "/users/", labelKey: "users", icon: UsersIcon },
  // Deliberately not sudoOnly: GET /api/tickets is requireAdmin, not
  // requireSudo - a reseller answers their own customers' tickets, and the
  // backend already scopes the list to the users they own.
  { key: "tickets", href: "/tickets/", labelKey: "rapido.tickets.nav", icon: TicketIcon },
  { key: "hosts", href: "/hosts/", labelKey: "rapido.hosts.nav", icon: GlobeAltIcon, sudoOnly: true },
  { key: "admins", href: "/admins/", labelKey: "rapido.admins.nav", icon: ShieldCheckIcon, sudoOnly: true },
  // Not sudoOnly: every admin can use their own reusable presets, not just
  // sudo - the backend's own GET /api/user_template is requireAdmin, not
  // requireSudo (only the write endpoints are sudo-gated).
  { key: "templates", href: "/templates/", labelKey: "rapido.templates.nav", icon: DocumentDuplicateIcon },
  {
    key: "integrations",
    href: "/integrations/",
    labelKey: "rapido.integrations.nav",
    icon: PuzzlePieceIcon,
    sudoOnly: true,
  },
];

export const RapidoShell: FC<PropsWithChildren<{ active: RapidoNavKey }>> = ({
  active,
  children,
}) => {
  const navigate = useNavigate();
  const { t, i18n } = useTranslation();
  const { data, isSuccess, isPending } = useCurrentAdminQuery();

  // Fail closed: while /admin is still in flight the admin is treated as
  // non-sudo, so a link the backend would answer with 403 is never shown.
  const isSudo = !isPending && isSuccess && !!data?.is_sudo;
  const navItems = NAV_ITEMS.filter((item) => !item.sudoOnly || isSudo);

  const logout = () => {
    removeAuthToken();
    navigate("/login");
  };

  return (
    <div
      dir={dirOf(i18n.language)}
      // `lang` is what tells the shaper this run is Persian, which is how the
      // browser picks Vazirmatn's Arabic subset over a device fallback for
      // codepoints several fonts could claim. `font-sans` states the stack
      // rather than inheriting it from a theme, so the Rapido pages keep
      // their typography no matter what.
      lang={i18n.language}
      className="min-h-screen bg-rapido-bg font-sans text-rapido-text"
    >
      <div className="flex">
        <aside className="sticky top-0 hidden h-screen w-56 shrink-0 flex-col border-e border-rapido-border p-4 md:flex">
          <div className="mb-6 flex items-center gap-2 px-2">
            <Logo className="h-6 w-6 text-rapido-accent" />
            <span className="text-lg font-bold">Rapido</span>
          </div>
          <nav className="flex flex-col gap-1">
            {navItems.map((item) => (
              <NavLink key={item.key} href={item.href} active={active === item.key} icon={item.icon}>
                {t(item.labelKey)}
              </NavLink>
            ))}
          </nav>
          <div className="mt-auto flex flex-col gap-1">
            <LanguageSwitcher openDirection="up" />
            <button
              onClick={logout}
              className="rounded-lg px-3 py-2 text-start text-sm font-medium text-rapido-muted transition-colors hover:bg-rapido-raised hover:text-rapido-text"
            >
              {t("header.logout")}
            </button>
          </div>
        </aside>

        {/* min-w-0: a flex item defaults to min-width:auto, so one wide child
            (a long subscription URL, a chart) would stretch this column past
            the viewport and give the whole page a horizontal scrollbar. */}
        <div className="flex min-h-screen min-w-0 flex-1 flex-col">
          {/* The nav sits on its own full-width row and wraps. Kept on one line
              it overflowed a 390px phone as soon as the labels were Persian
              (most of the userbase). The switcher is here too: the sidebar
              that used to be its only home is display:none below md, so on a
              phone there was no way to change language at all. */}
          <header className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-rapido-border px-4 py-3 md:hidden">
            <div className="flex items-center gap-2">
              <Logo className="h-6 w-6 shrink-0 text-rapido-accent" />
              <span className="text-lg font-bold">Rapido</span>
            </div>
            <LanguageSwitcher openDirection="down" align="end" />
            <nav className="flex w-full flex-wrap items-center gap-1 text-sm">
              {navItems.map((item) => (
                <NavLink key={item.key} href={item.href} active={active === item.key} icon={item.icon}>
                  {t(item.labelKey)}
                </NavLink>
              ))}
              <button
                onClick={logout}
                className="ms-auto rounded-lg px-3 py-2 text-sm font-medium text-rapido-muted transition-colors hover:bg-rapido-raised hover:text-rapido-text"
              >
                {t("header.logout")}
              </button>
            </nav>
          </header>

          <main className="min-w-0 flex-1">{children}</main>
        </div>
      </div>
    </div>
  );
};
