// Mirrors internal/httpapi/backup.go's backupInfo - a filename this backend
// generated is the identity, not a database id: this feature has no table
// (see backup.go's own doc comment), just a directory listing.
export type Backup = {
  filename: string;
  size_bytes: number;
  created_at: string;
};

// Mirrors restoreResultDTO - the from-existing-backup restore endpoint
// always returns this; the upload endpoint returns it only when the
// uploaded file turned out to be a native Postgres backup (see
// LegacyImportResult for the other case that same endpoint can return).
export type RestoreResult = {
  safety_backup: Backup;
  detail: string;
};

// Mirrors legacyImportResultDTO - what the upload endpoint returns when
// the uploaded file was recognized as a legacy panel export (from an older
// panel, more formats later) instead of a native backup. No `detail`
// field - the counts and warnings are the actual summary here.
export type LegacyImportResult = {
  safety_backup: Backup;
  admins_imported: number;
  users_imported: number;
  hosts_imported: number;
  inbounds_imported: number;
  warnings: string[];
};

export const isLegacyImportResult = (
  r: RestoreResult | LegacyImportResult
): r is LegacyImportResult => "admins_imported" in r;
