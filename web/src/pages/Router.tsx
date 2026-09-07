import { Suspense, lazy } from "react";
import { Navigate, createHashRouter } from "react-router-dom";
import { fetchCurrentAdmin } from "hooks/useCurrentAdminQuery";
import { queryClient, queryKeys } from "utils/queryClient";
import { SudoOnly } from "rapido-ui/SudoOnly";
import { Login } from "./Login";

// A failed /admin fetch (expired/missing token) throws, which react-router
// routes to `errorElement: <Login/>` below - the same "inline, not a global
// interceptor" 401 handling the old dashboard used, so an admin mid-edit on
// some other tab is never yanked out from under themselves by a background
// 401 on an unrelated request.
//
// Seeds the query cache with the result too (a small improvement over the
// old dashboard's loader, which just returned the fetch and let
// Shell/SudoOnly's own useGetUser hit the network again) - useCurrentAdminQuery
// reads the exact same queryKey, so Shell/SudoOnly render with data already
// in hand instead of a second round trip on every navigation.
const fetchAdminLoader = async () => {
  const data = await fetchCurrentAdmin();
  queryClient.setQueryData(queryKeys.currentAdmin, data);
  return data;
};

// Lazy-loaded so each page's dependencies are a separate chunk that Login
// never has to fetch or evaluate - a page-specific bug can't take down
// unrelated routes.
const RapidoHome = lazy(() => import("./RapidoHome"));
const UsersPage = lazy(() => import("./UsersPage"));
const TicketsPage = lazy(() => import("./TicketsPage"));
const HostsPage = lazy(() => import("./HostsPage"));
const CoreConfigPage = lazy(() => import("./CoreConfigPage"));
const AdminsPage = lazy(() => import("./AdminsPage"));
const IntegrationsPage = lazy(() => import("./IntegrationsPage"));
const UserTemplatesPage = lazy(() => import("./UserTemplatesPage"));
const NodesPage = lazy(() => import("./NodesPage"));
const MonitoringPage = lazy(() => import("./MonitoringPage"));

export const router = createHashRouter([
  {
    path: "/",
    element: (
      <Suspense fallback={null}>
        <RapidoHome />
      </Suspense>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/users/",
    element: (
      <Suspense fallback={null}>
        <UsersPage />
      </Suspense>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    // Not sudo-gated: GET /api/user_template is requireAdmin, not
    // requireSudo - every admin can browse/use these presets, only the
    // sudo-only write endpoints (create/edit/delete) are restricted, which
    // UserTemplatesPage itself enforces by hiding those actions.
    path: "/templates/",
    element: (
      <Suspense fallback={null}>
        <UserTemplatesPage />
      </Suspense>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    // Not sudo-gated: GET /api/tickets is requireAdmin, not requireSudo -
    // every admin can read/answer their own customers' tickets, scoped
    // server-side (internal/httpapi/tickets.go's scopedAdminID); only the
    // `owner` column is sudo-conditional, and TicketsAdmin itself hides it.
    path: "/tickets/",
    element: (
      <Suspense fallback={null}>
        <TicketsPage />
      </Suspense>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/hosts/",
    element: (
      <SudoOnly>
        <Suspense fallback={null}>
          <HostsPage />
        </Suspense>
      </SudoOnly>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/core-config/",
    element: (
      <SudoOnly>
        <Suspense fallback={null}>
          <CoreConfigPage />
        </Suspense>
      </SudoOnly>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/admins/",
    element: (
      <SudoOnly>
        <Suspense fallback={null}>
          <AdminsPage />
        </Suspense>
      </SudoOnly>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/integrations/",
    element: (
      <SudoOnly>
        <Suspense fallback={null}>
          <IntegrationsPage />
        </Suspense>
      </SudoOnly>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/nodes/",
    element: (
      <SudoOnly>
        <Suspense fallback={null}>
          <NodesPage />
        </Suspense>
      </SudoOnly>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/monitoring/",
    element: (
      <SudoOnly>
        <Suspense fallback={null}>
          <MonitoringPage />
        </Suspense>
      </SudoOnly>
    ),
    errorElement: <Login />,
    loader: fetchAdminLoader,
  },
  {
    path: "/login/",
    element: <Login />,
  },
  {
    // Catches any other hash path - old bookmarks to retired routes
    // (Settings, from the previous dashboard, or a typo'd URL) land here
    // instead of react-router's default error UI. Nodes/Monitoring used to
    // be in that boat too; both now have real Go-backed pages above.
    path: "*",
    element: <Navigate to="/" replace />,
  },
]);
