import { User } from "types/User";

// The backend returns a bare "/sub/<token>" path whenever no URL prefix is
// configured, which is useless once copied out of the panel - so resolve it
// against the panel's own origin.
export const absoluteSubscriptionUrl = (url: string): string =>
  url.startsWith("/") ? window.location.origin + url : url;

// Every address this user's subscription answers on, in the order the server
// listed them (the one to show first is first). An older backend only sends
// `subscription_url`, so that stays the fallback.
export const subscriptionUrlsOf = (
  user: Pick<User, "subscription_url" | "subscription_urls">
): string[] => {
  const listed = user.subscription_urls?.length
    ? user.subscription_urls
    : [user.subscription_url];
  const seen = new Set<string>();
  const urls: string[] = [];
  for (const raw of listed) {
    const url = absoluteSubscriptionUrl(raw);
    if (url && !seen.has(url)) {
      seen.add(url);
      urls.push(url);
    }
  }
  return urls;
};

export const hostOf = (url: string): string => {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
};
