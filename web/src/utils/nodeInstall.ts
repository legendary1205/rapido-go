// The one-line command that installs a freshly created node: the installer
// script is served by this very panel, and the node's one-time setup blob is
// its only argument. The blob is base64 so it holds no quote, but the command
// is pasted into a root shell, so the value is escaped for single quotes
// regardless of what it is assumed to contain.
export const buildNodeInstallCommand = (origin: string, setupBlob: string): string => {
  const base = origin.replace(/\/+$/, "");
  const quoted = `'${setupBlob.replace(/'/g, `'\\''`)}'`;
  return `curl -fsSL ${base}/install/node.sh | sudo bash -s -- ${quoted}`;
};
