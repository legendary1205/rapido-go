import { create } from "zustand";
import { User } from "types/User";
import { UsersFilters } from "hooks/useUsersQuery";
import { getUsersPerPageLimitSize } from "utils/userPreferenceStorage";

// Ephemeral UI-only state for the Users page - which modal is open, and the
// list's current filters. All server data (the list itself, mutations) moved
// to hooks/useUsersQuery.ts's TanStack Query hooks per the plan; zustand's
// only job left here is coordinating UsersTable/UserFormModal/
// UserActionModals, which live in separate files and all need to react to
// "an admin clicked Edit on this row" etc. This replaces the data-fetching
// half of the old contexts/DashboardContext.tsx - the UI-state half is what
// survives.
type UsersUiState = {
  filters: UsersFilters;
  setFilters: (patch: Partial<UsersFilters>) => void;

  isCreatingNewUser: boolean;
  setCreatingNewUser: (open: boolean) => void;

  editingUser: User | null;
  setEditingUser: (user: User | null) => void;

  deletingUser: User | null;
  setDeletingUser: (user: User | null) => void;

  resetUsageUser: User | null;
  setResetUsageUser: (user: User | null) => void;

  revokeSubscriptionUser: User | null;
  setRevokeSubscriptionUser: (user: User | null) => void;

  // The old dashboard's separate QR-grid and copy-link modals merge into one
  // (see the plan's Users notes): the backend only ever returns one link
  // (`subscription_url`, no `links[]`), so there is nothing left to show a
  // *grid* of - one QR code beside one copyable URL covers it.
  subscriptionLinkUser: User | null;
  setSubscriptionLinkUser: (user: User | null) => void;
};

export const useUsersUiStore = create<UsersUiState>((set) => ({
  filters: { sort: "-created_at", limit: getUsersPerPageLimitSize() },
  setFilters: (patch) =>
    set((state) => ({ filters: { ...state.filters, ...patch } })),

  isCreatingNewUser: false,
  setCreatingNewUser: (open) => set({ isCreatingNewUser: open }),

  editingUser: null,
  setEditingUser: (user) => set({ editingUser: user }),

  deletingUser: null,
  setDeletingUser: (user) => set({ deletingUser: user }),

  resetUsageUser: null,
  setResetUsageUser: (user) => set({ resetUsageUser: user }),

  revokeSubscriptionUser: null,
  setRevokeSubscriptionUser: (user) => set({ revokeSubscriptionUser: user }),

  subscriptionLinkUser: null,
  setSubscriptionLinkUser: (user) => set({ subscriptionLinkUser: user }),
}));
