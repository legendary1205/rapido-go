import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch, fetcher } from "service/http";
import { Backup, LegacyImportResult, RestoreResult } from "types/Backup";
import { queryKeys } from "utils/queryClient";

export const useBackupsQuery = () =>
  useQuery({
    queryKey: queryKeys.backups,
    queryFn: () => fetch<Backup[]>("/settings/backup"),
  });

const invalidateBackups = (queryClient: ReturnType<typeof useQueryClient>) =>
  queryClient.invalidateQueries({ queryKey: queryKeys.backups });

export const useCreateBackupMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => fetch<Backup>("/settings/backup", { method: "POST" }),
    onSuccess: () => invalidateBackups(queryClient),
  });
};

export const useDeleteBackupMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (filename: string) =>
      fetch(`/settings/backup/${encodeURIComponent(filename)}`, { method: "DELETE" }),
    onSuccess: () => invalidateBackups(queryClient),
  });
};

// Both restore paths invalidate every query that could be reading now-
// replaced data, not just the backups list - the whole database (users,
// admins, hosts, ...) was just swapped out from under every other page.
const invalidateEverythingAfterRestore = (queryClient: ReturnType<typeof useQueryClient>) =>
  queryClient.invalidateQueries();

export const useRestoreBackupMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (filename: string) =>
      fetch<RestoreResult>(`/settings/backup/${encodeURIComponent(filename)}/restore`, {
        method: "POST",
        body: { confirm: true },
      }),
    onSuccess: () => invalidateEverythingAfterRestore(queryClient),
  });
};

// The upload can turn out to be either a native restore or a legacy-panel
// import (the backend decides which by looking at what's actually inside
// the file - see internal/httpapi/legacyimport.go's detectUploadFormat) -
// the response shape tells the caller which one happened after the fact,
// via isLegacyImportResult.
export const useRestoreUploadMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (file: File) => {
      const form = new FormData();
      form.append("confirm", "true");
      form.append("file", file);
      return fetch<RestoreResult | LegacyImportResult>("/settings/backup/restore-upload", {
        method: "POST",
        body: form,
      });
    },
    onSuccess: () => invalidateEverythingAfterRestore(queryClient),
  });
};

// Not a query/mutation - a one-shot side effect triggered from a click
// handler. Fetches the file as a blob (the endpoint requires the same
// bearer token every other request carries, so a plain <a href> can't be
// used) and hands it to the browser via a synthetic <a download> click,
// the standard way to save an authenticated fetch response as a file.
export const downloadBackup = async (filename: string): Promise<void> => {
  const blob = await fetcher<Blob, "blob">(`/settings/backup/${encodeURIComponent(filename)}`, {
    responseType: "blob",
  });
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
};
