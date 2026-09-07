import { FetchOptions, ResponseType, $fetch as ohMyFetch } from "ofetch";
import { getAuthToken } from "utils/authStorage";

export const $fetch = ohMyFetch.create({
  baseURL: import.meta.env.VITE_BASE_API,
});

// Generic over ResponseType (defaulting to "json", same as every existing
// call site) rather than hardcoded to it - hooks/useBackupsQuery.ts's
// downloadBackup is the first caller that needs responseType: "blob" to
// pull down a real file instead of a parsed JSON body.
export const fetcher = <T = any, R extends ResponseType = "json">(
  url: string,
  ops: FetchOptions<R> = {} as FetchOptions<R>
) => {
  const token = getAuthToken();
  if (token) {
    ops["headers"] = {
      ...(ops?.headers || {}),
      Authorization: `Bearer ${getAuthToken()}`,
    };
  }
  return $fetch<T, R>(url, ops);
};

export const fetch = fetcher;
