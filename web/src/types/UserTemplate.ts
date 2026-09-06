// Mirrors internal/httpapi/user_template.go's userTemplateDTO/
// userTemplateWriteRequest. Per the plan's key fact #5: unlike User's
// data_limit/expire, these two fields carry no null-means-unlimited
// normalization on the backend - 0 is a real, literal 0, not "unlimited".
// There is also no "apply this template to a new user" endpoint yet, so this
// type only backs an independent CRUD page, not the user-creation form.
export type UserTemplate = {
  id: number;
  name: string;
  data_limit: number;
  expire_duration: number;
  username_prefix: string | null;
  username_suffix: string | null;
  inbounds: Record<string, string[]>;
};

export type UserTemplateWritePayload = {
  name: string;
  data_limit: number;
  expire_duration: number;
  username_prefix?: string | null;
  username_suffix?: string | null;
  inbounds: Record<string, string[]>;
};
