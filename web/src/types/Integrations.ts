// Mirrors internal/httpapi/settings.go's integrationSettingsDTO.
export type IntegrationSettingsStatus = {
  reseller_api_enabled: boolean;
  reseller_api_secret?: string | null; // masked
  reseller_api_url?: string | null;
  reseller_api_license?: string | null; // masked
  telegram_enabled: boolean;
  telegram_api_token?: string | null; // masked
  telegram_admin_ids: number[];
  telegram_proxy_url?: string | null; // masked
  telegram_logger_channel_id?: number | null;
  telegram_logger_topic_id?: number | null;
  telegram_default_vless_flow?: string | null;
  webhook_addresses: string[];
  webhook_secret?: string | null; // masked
  discord_webhook_url?: string | null; // masked
  updated_at?: string | null;
};

// The tri-state PATCH body PUT /api/settings/integrations expects: a key
// absent from the JSON body leaves that setting untouched, a key present
// with value null clears the DB override back to the .env default, and a key
// present with a value overwrites it. See utils/buildIntegrationsPatch.ts for
// the pure function that assembles this from the form's draft state.
export type IntegrationsPatch = Record<string, unknown>;
