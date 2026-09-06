import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import { UserTemplate, UserTemplateWritePayload } from "types/UserTemplate";
import { queryKeys } from "utils/queryClient";

export const useUserTemplatesQuery = () =>
  useQuery({
    queryKey: queryKeys.userTemplates,
    queryFn: () => fetch<UserTemplate[]>("/user_template"),
  });

const invalidateUserTemplates = (
  queryClient: ReturnType<typeof useQueryClient>
) => queryClient.invalidateQueries({ queryKey: queryKeys.userTemplates });

export const useCreateUserTemplateMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: UserTemplateWritePayload) =>
      fetch<UserTemplate>("/user_template", { method: "POST", body }),
    onSuccess: () => invalidateUserTemplates(queryClient),
  });
};

export const useUpdateUserTemplateMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      body,
    }: {
      id: number;
      body: UserTemplateWritePayload;
    }) => fetch<UserTemplate>(`/user_template/${id}`, { method: "PUT", body }),
    onSuccess: () => invalidateUserTemplates(queryClient),
  });
};

export const useDeleteUserTemplateMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      fetch(`/user_template/${id}`, { method: "DELETE" }),
    onSuccess: () => invalidateUserTemplates(queryClient),
  });
};
