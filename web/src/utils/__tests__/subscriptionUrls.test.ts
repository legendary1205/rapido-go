import { describe, expect, it } from "vitest";
import { hostOf, subscriptionUrlsOf } from "utils/subscriptionUrls";

const user = (subscription_url: string, subscription_urls?: string[]) => ({
  subscription_url,
  subscription_urls,
});

describe("subscriptionUrlsOf", () => {
  it("keeps the server's order, so the address listed first is shown first", () => {
    expect(
      subscriptionUrlsOf(
        user("https://sub.officialvpn.shop/sub/T", [
          "https://sub.ts01.ir/sub/T",
          "https://sub.officialvpn.shop/sub/T",
        ])
      )
    ).toEqual(["https://sub.ts01.ir/sub/T", "https://sub.officialvpn.shop/sub/T"]);
  });

  it("falls back to the single subscription_url from a backend that predates the list", () => {
    expect(subscriptionUrlsOf(user("https://sub.officialvpn.shop/sub/T"))).toEqual([
      "https://sub.officialvpn.shop/sub/T",
    ]);
    expect(subscriptionUrlsOf(user("https://sub.officialvpn.shop/sub/T", []))).toEqual([
      "https://sub.officialvpn.shop/sub/T",
    ]);
  });

  it("drops duplicates", () => {
    expect(
      subscriptionUrlsOf(user("https://a/sub/T", ["https://a/sub/T", "https://a/sub/T"]))
    ).toEqual(["https://a/sub/T"]);
  });

  it("resolves a relative path against the panel's own origin", () => {
    expect(subscriptionUrlsOf(user("/sub/T"))).toEqual([window.location.origin + "/sub/T"]);
  });
});

describe("hostOf", () => {
  it("returns the host, and the input itself when it is not a URL", () => {
    expect(hostOf("https://sub.ts01.ir/sub/T")).toBe("sub.ts01.ir");
    expect(hostOf("not a url")).toBe("not a url");
  });
});
