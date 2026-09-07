// Mirrors internal/httpapi/backup.go's backupInfo - a filename this backend
// generated is the identity, not a database id: this feature has no table
// (see backup.go's own doc comment), just a directory listing.
export type Backup = {
  filename: string;
  size_bytes: number;
  created_at: string;
};
